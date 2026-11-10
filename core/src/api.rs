use crate::state::{now_text, AppState};
use crate::updater;
use serde_json::{json, Value};
use std::sync::atomic::Ordering;
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};

pub async fn run(state: Arc<AppState>) -> Result<(), Box<dyn std::error::Error>> {
    let port = state.cfg.read().unwrap().control_port;
    let listener = TcpListener::bind(("127.0.0.1", port)).await?;
    println!("[core] 控制接口已启动: http://127.0.0.1:{port}/api/status");
    loop {
        let (stream, _peer) = listener.accept().await?;
        let st = state.clone();
        tokio::spawn(async move {
            let _ = handle_conn(st, stream).await;
        });
    }
}

struct Request {
    method: String,
    path: String,
    body: Vec<u8>,
}

async fn handle_conn(state: Arc<AppState>, mut stream: TcpStream) -> std::io::Result<()> {
    let mut buf: Vec<u8> = Vec::new();
    let mut tmp = [0u8; 4096];
    let mut header_end = 0usize;
    let mut content_length = 0usize;
    loop {
        let n = stream.read(&mut tmp).await?;
        if n == 0 {
            break;
        }
        buf.extend_from_slice(&tmp[..n]);
        if header_end == 0 {
            if let Some(pos) = find_subsequence(&buf, b"\r\n\r\n") {
                header_end = pos + 4;
                let head = String::from_utf8_lossy(&buf[..pos]).to_string();
                for line in head.lines().skip(1) {
                    if let Some((k, v)) = line.split_once(':') {
                        if k.trim().eq_ignore_ascii_case("content-length") {
                            content_length = v.trim().parse().unwrap_or(0);
                        }
                    }
                }
            }
        }
        if header_end > 0 && buf.len() >= header_end + content_length {
            break;
        }
        if buf.len() > 2 * 1024 * 1024 {
            break;
        }
    }

    if header_end == 0 {
        return Ok(());
    }
    let head = String::from_utf8_lossy(&buf[..header_end]).to_string();
    let mut lines = head.lines();
    let request_line = lines.next().unwrap_or_default();
    let mut parts = request_line.split_whitespace();
    let method = parts.next().unwrap_or("").to_ascii_uppercase();
    let target = parts.next().unwrap_or("/");
    let path = target.split('?').next().unwrap_or("/").to_string();
    let query = target.split_once('?').map(|(_, q)| q.to_string()).unwrap_or_default();
    let body = buf[header_end..].to_vec();

    if method == "OPTIONS" {
        return respond(&mut stream, 204, Value::Null).await;
    }

    let req = Request { method: method.clone(), path: path.clone(), body };

    let result = route(&state, &req, &query).await;
    match result {
        Ok((status, value)) => respond(&mut stream, status, value).await,
        Err(err) => respond(&mut stream, 500, json!({ "error": err })).await,
    }
}

async fn route(
    state: &Arc<AppState>,
    req: &Request,
    query: &str,
) -> Result<(u16, Value), String> {
    let path = req.path.as_str();

    if path == "/api/health" {
        return Ok((200, json!({
            "ok": true,
            "version": env!("CARGO_PKG_VERSION"),
            "pid": std::process::id()
        })));
    }

    if path == "/api/status" && req.method == "GET" {
        return Ok((200, status_json(state)));
    }

    if path == "/api/filters" && req.method == "GET" {
        let cfg = state.cfg.read().unwrap();
        let dir = cfg.filters_dir(&state.config_path);
        let filters: Vec<Value> = cfg
            .filters
            .iter()
            .map(|f| {
                let file = dir.join(format!("{}.txt", f.id));
                json!({
                    "id": f.id,
                    "name": f.name,
                    "url": f.url,
                    "enabled": f.enabled,
                    "auto_update": f.auto_update,
                    "update_interval_minutes": f.update_interval_minutes,
                    "last_update": f.last_update,
                    "last_error": f.last_error,
                    "rule_count": f.rule_count,
                    "builtin": f.builtin,
                    "kind": f.kind,
                    "cached": file.exists(),
                })
            })
            .collect();
        return Ok((200, json!({ "filters": filters })));
    }

    if path == "/api/filters" && req.method == "POST" {
        let body = parse_body(req)?;
        let name = body["name"].as_str().unwrap_or("").trim().to_string();
        let url = body["url"].as_str().unwrap_or("").trim().to_string();
        if url.is_empty() || !url.starts_with("http") {
            return Err("订阅地址无效".to_string());
        }
        let id = format!("f{}", crate::state::unix_now());
        {
            let mut cfg = state.cfg.write().unwrap();
            if let Some(existing) = cfg.filters.iter().find(|f| f.url == url) {
                return Ok((200, json!({ "id": existing.id, "existing": true })));
            }
            cfg.filters.push(crate::config::FilterConfig {
                id: id.clone(),
                name: if name.is_empty() { url.clone() } else { name },
                url,
                enabled: true,
                auto_update: true,
                update_interval_minutes: 720,
                last_update: None,
                last_update_ts: 0,
                last_error: None,
                rule_count: 0,
                builtin: false,
                kind: "subscription".to_string(),
            });
        }
        state.save_config();
        let st = state.clone();
        let fid = id.clone();
        tokio::spawn(async move {
            if let Err(err) = updater::update_filter(st.clone(), &fid).await {
                updater::mark_error(&st, &fid, &err);
            }
        });
        return Ok((200, json!({ "id": id })));
    }

    if path.starts_with("/api/filters/") && req.method == "DELETE" {
        let id = path.trim_start_matches("/api/filters/").to_string();
        {
            let mut cfg = state.cfg.write().unwrap();
            cfg.filters.retain(|f| f.id != id);
        }
        let dir = {
            let cfg = state.cfg.read().unwrap();
            cfg.filters_dir(&state.config_path)
        };
        let _ = std::fs::remove_file(dir.join(format!("{id}.txt")));
        state.save_config();
        state.reload_engine();
        return Ok((200, json!({ "ok": true })));
    }

    if path == "/api/filters/update" && req.method == "POST" {
        let st = state.clone();
        tokio::spawn(async move {
            updater::update_all(st).await;
        });
        return Ok((200, json!({ "ok": true, "queued": true })));
    }

    if path.starts_with("/api/filters/") && path.ends_with("/update") && req.method == "POST" {
        let id = path
            .trim_start_matches("/api/filters/")
            .trim_end_matches("/update")
            .to_string();
        let st = state.clone();
        let fid = id.clone();
        tokio::spawn(async move {
            if let Err(err) = updater::update_filter(st.clone(), &fid).await {
                updater::mark_error(&st, &fid, &err);
            }
        });
        return Ok((200, json!({ "ok": true, "queued": true })));
    }

    if path.starts_with("/api/filters/") && req.method == "PATCH" {
        // 启用/停用、自动更新开关
        let id = path.trim_start_matches("/api/filters/").to_string();
        let body = parse_body(req)?;
        {
            let mut cfg = state.cfg.write().unwrap();
            if let Some(f) = cfg.filters.iter_mut().find(|f| f.id == id) {
                if let Some(v) = body["enabled"].as_bool() {
                    f.enabled = v;
                }
                if let Some(v) = body["auto_update"].as_bool() {
                    f.auto_update = v;
                }
                if let Some(v) = body["update_interval_minutes"].as_u64() {
                    f.update_interval_minutes = v.max(5);
                }
                if let Some(v) = body["name"].as_str() {
                    if !v.trim().is_empty() {
                        f.name = v.trim().to_string();
                    }
                }
            } else {
                return Err("过滤器不存在".to_string());
            }
        }
        state.save_config();
        state.reload_engine();
        state.clear_cache();
        return Ok((200, json!({ "ok": true })));
    }

    if path == "/api/protection" && req.method == "POST" {
        let body = parse_body(req)?;
        let enabled = body["enabled"].as_bool().unwrap_or(true);
        {
            let mut cfg = state.cfg.write().unwrap();
            cfg.protection_enabled = enabled;
        }
        state.save_config();
        state.clear_cache();
        println!("[core] 保护状态: {}", if enabled { "开启" } else { "关闭" });
        return Ok((200, json!({ "ok": true, "protection": enabled })));
    }

    if path == "/api/rules" && req.method == "GET" {
        let cfg = state.cfg.read().unwrap();
        return Ok((200, json!({ "rules": cfg.custom_rules })));
    }

    if path == "/api/rules" && req.method == "POST" {
        let body = parse_body(req)?;
        let rules: Vec<String> = body["rules"]
            .as_array()
            .map(|arr| {
                arr.iter()
                    .filter_map(|v| v.as_str().map(|s| s.to_string()))
                    .collect()
            })
            .unwrap_or_default();
        {
            let mut cfg = state.cfg.write().unwrap();
            cfg.custom_rules = rules;
        }
        state.save_config();
        state.reload_engine();
        state.clear_cache();
        return Ok((200, json!({ "ok": true })));
    }

    if path == "/api/logs" && req.method == "GET" {
        let limit = query
            .split('&')
            .find_map(|kv| kv.strip_prefix("limit="))
            .and_then(|v| v.parse::<usize>().ok())
            .unwrap_or(100)
            .min(500);
        let log = state.log.lock().unwrap();
        let items: Vec<_> = log.iter().take(limit).cloned().collect();
        return Ok((200, json!({ "logs": items })));
    }

    if path == "/api/logs" && req.method == "DELETE" {
        state.log.lock().unwrap().clear();
        return Ok((200, json!({ "ok": true })));
    }

    if path == "/api/stats/top" && req.method == "GET" {
        let map = state.stats.blocked_domains.lock().unwrap();
        let mut items: Vec<(String, u64)> = map.iter().map(|(k, v)| (k.clone(), *v)).collect();
        items.sort_by(|a, b| b.1.cmp(&a.1));
        items.truncate(20);
        let list: Vec<Value> = items
            .into_iter()
            .map(|(domain, count)| json!({ "domain": domain, "count": count }))
            .collect();
        return Ok((200, json!({ "top": list })));
    }

    if path == "/api/cache/clear" && req.method == "POST" {
        state.clear_cache();
        return Ok((200, json!({ "ok": true })));
    }

    if path == "/api/reload" && req.method == "POST" {
        let cfg = crate::config::load_or_create(&state.config_path);
        *state.cfg.write().unwrap() = cfg;
        state.reload_engine();
        state.clear_cache();
        return Ok((200, json!({ "ok": true })));
    }

    Ok((404, json!({ "error": "not found", "path": path })))
}

fn status_json(state: &Arc<AppState>) -> Value {
    let cfg = state.cfg.read().unwrap();
    let stats = &state.stats;
    let total = stats.total.load(Ordering::Relaxed);
    let blocked = stats.blocked.load(Ordering::Relaxed);
    let cache_len = state.cache.lock().unwrap().len();
    let filters: Vec<Value> = cfg
        .filters
        .iter()
        .map(|f| {
            json!({
                "id": f.id,
                "name": f.name,
                "enabled": f.enabled,
                "rule_count": f.rule_count,
                "last_update": f.last_update,
                "last_error": f.last_error,
                "auto_update": f.auto_update,
            })
        })
        .collect();

    json!({
        "version": env!("CARGO_PKG_VERSION"),
        "protection": cfg.protection_enabled,
        "listen_address": cfg.listen_address,
        "listen_port": cfg.listen_port,
        "control_port": cfg.control_port,
        "block_mode": cfg.block_mode,
        "upstreams": cfg.upstreams,
        "uptime_seconds": stats.uptime_seconds(),
        "queries": {
            "total": total,
            "blocked": blocked,
            "allowed": stats.allowed.load(Ordering::Relaxed),
            "cache_hits": stats.cache_hits.load(Ordering::Relaxed),
            "upstream_errors": stats.upstream_errors.load(Ordering::Relaxed),
        },
        "blocked_percent": if total > 0 { (blocked as f64 / total as f64 * 100.0 * 10.0).round() / 10.0 } else { 0.0 },
        "cache_size": cache_len,
        "rules_total": state.effective_rules.load(Ordering::Relaxed),
        "custom_rules": cfg.custom_rules.len(),
        "filters": filters,
        "time": now_text(),
    })
}

fn parse_body(req: &Request) -> Result<Value, String> {
    if req.body.is_empty() {
        return Ok(json!({}));
    }
    serde_json::from_slice(&req.body).map_err(|e| format!("请求体不是合法 JSON: {e}"))
}

async fn respond(stream: &mut TcpStream, status: u16, value: Value) -> std::io::Result<()> {
    let body = if value.is_null() {
        String::new()
    } else {
        value.to_string()
    };
    let reason = match status {
        200 => "OK",
        204 => "No Content",
        404 => "Not Found",
        500 => "Internal Server Error",
        _ => "OK",
    };
    let head = format!(
        "HTTP/1.1 {status} {reason}\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: {}\r\nAccess-Control-Allow-Origin: *\r\nAccess-Control-Allow-Methods: GET,POST,PATCH,DELETE,OPTIONS\r\nAccess-Control-Allow-Headers: Content-Type\r\nConnection: close\r\n\r\n",
        body.len()
    );
    stream.write_all(head.as_bytes()).await?;
    stream.write_all(body.as_bytes()).await?;
    stream.flush().await?;
    Ok(())
}

fn find_subsequence(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}
