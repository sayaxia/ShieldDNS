use crate::engine::Engine;
use crate::state::{now_text, AppState};
use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::Duration;

pub async fn update_filter(state: Arc<AppState>, id: &str) -> Result<usize, String> {
    let (url, cfg_dir) = {
        let cfg = state.cfg.read().unwrap();
        let filter = cfg
            .filters
            .iter()
            .find(|f| f.id == id)
            .ok_or_else(|| format!("过滤器不存在: {id}"))?;
        (filter.url.clone(), cfg.filters_dir(&state.config_path))
    };

    println!("[core] 正在更新过滤器 {id}: {url}");
    let resp = state
        .client
        .get(&url)
        .send()
        .await
        .map_err(|e| format!("下载失败: {e}"))?;
    if !resp.status().is_success() {
        return Err(format!("HTTP {}", resp.status()));
    }
    let body = resp.text().await.map_err(|e| format!("读取失败: {e}"))?;
    if body.len() > 64 * 1024 * 1024 {
        return Err("规则文件过大（>64MB）".to_string());
    }
    if body.trim().is_empty() {
        return Err("规则内容为空".to_string());
    }

    std::fs::create_dir_all(&cfg_dir).map_err(|e| format!("创建目录失败: {e}"))?;
    let path = cfg_dir.join(format!("{id}.txt"));
    let tmp = cfg_dir.join(format!("{id}.txt.tmp"));
    std::fs::write(&tmp, body.as_bytes()).map_err(|e| format!("写入失败: {e}"))?;
    let _ = std::fs::remove_file(&path);
    std::fs::rename(&tmp, &path).map_err(|e| format!("替换失败: {e}"))?;

    let count = Engine::build(&[(body.clone(), true)], &[]).parsed_rules;

    {
        let mut cfg = state.cfg.write().unwrap();
        if let Some(filter) = cfg.filters.iter_mut().find(|f| f.id == id) {
            filter.last_update = Some(now_text());
            filter.last_update_ts = crate::state::unix_now();
            filter.last_error = None;
            filter.rule_count = count;
        }
    }
    state.save_config();
    state.reload_engine();
    state.clear_cache();
    println!("[core] 过滤器 {id} 更新完成，共 {count} 条规则");
    Ok(count)
}

pub fn mark_error(state: &Arc<AppState>, id: &str, err: &str) {
    {
        let mut cfg = state.cfg.write().unwrap();
        if let Some(filter) = cfg.filters.iter_mut().find(|f| f.id == id) {
            filter.last_error = Some(err.to_string());
        }
    }
    state.save_config();
}

pub async fn update_all(state: Arc<AppState>) -> usize {
    let ids: Vec<String> = {
        let cfg = state.cfg.read().unwrap();
        cfg.filters
            .iter()
            .filter(|f| f.enabled && f.kind == "subscription")
            .map(|f| f.id.clone())
            .collect()
    };
    let mut ok = 0usize;
    for id in ids {
        match update_filter(state.clone(), &id).await {
            Ok(_) => ok += 1,
            Err(err) => {
                eprintln!("[core] 更新 {id} 失败: {err}");
                mark_error(&state, &id, &err);
            }
        }
    }
    ok
}

pub async fn auto_update_loop(state: Arc<AppState>) {
    // 启动后先等 5 秒，避免和面板首屏抢资源
    tokio::time::sleep(Duration::from_secs(5)).await;
    loop {
        let due: Vec<String> = {
            let cfg = state.cfg.read().unwrap();
            let now = crate::state::unix_now();
            cfg.filters
                .iter()
                .filter(|f| {
                    f.enabled
                        && f.auto_update
                        && f.kind == "subscription"
                        && (f.last_update_ts == 0
                            || now.saturating_sub(f.last_update_ts) >= f.update_interval_minutes * 60)
                })
                .map(|f| f.id.clone())
                .collect()
        };
        if !due.is_empty() {
            println!("[core] 自动更新到期的过滤器: {due:?}");
            for id in due {
                match update_filter(state.clone(), &id).await {
                    Ok(count) => println!("[core] 自动更新 {id} 成功，{count} 条"),
                    Err(err) => {
                        eprintln!("[core] 自动更新 {id} 失败: {err}");
                        mark_error(&state, &id, &err);
                    }
                }
            }
        }
        state.prune_cache();
        println!(
            "[core] 心跳 规则={} 查询={} 拦截={}",
            state.effective_rules.load(Ordering::Relaxed),
            state.stats.total.load(Ordering::Relaxed),
            state.stats.blocked.load(Ordering::Relaxed),
        );
        tokio::time::sleep(Duration::from_secs(60)).await;
    }
}
