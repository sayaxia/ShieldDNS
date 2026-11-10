use crate::state::{now_text, AppState, CacheEntry, LogEntry};
use hickory_proto::op::{Message, MessageType, ResponseCode};
use hickory_proto::rr::rdata::{A, AAAA};
use hickory_proto::rr::{RData, Record, RecordType};
use std::net::{Ipv4Addr, Ipv6Addr, SocketAddr};
use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::{Duration, Instant};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream, UdpSocket};

pub async fn run(state: Arc<AppState>) -> Result<(), Box<dyn std::error::Error>> {
    let (addr, port) = {
        let cfg = state.cfg.read().unwrap();
        (cfg.listen_address.clone(), cfg.listen_port)
    };
    let bind: SocketAddr = format!("{addr}:{port}").parse()?;
    let udp = Arc::new(UdpSocket::bind(bind).await?);
    let tcp = TcpListener::bind(bind).await?;
    println!("[core] DNS 服务已启动: udp/tcp {bind}");

    let udp_state = state.clone();
    tokio::spawn(async move {
        let mut buf = vec![0u8; 4096];
        loop {
            match udp.recv_from(&mut buf).await {
                Ok((len, peer)) => {
                    let payload = buf[..len].to_vec();
                    let socket = udp.clone();
                    let st = udp_state.clone();
                    tokio::spawn(async move {
                        if let Some(resp) = handle_query(st, payload, peer, false).await {
                            let _ = socket.send_to(&resp, peer).await;
                        }
                    });
                }
                Err(err) => {
                    eprintln!("[core] UDP 接收失败: {err}");
                    tokio::time::sleep(Duration::from_millis(200)).await;
                }
            }
        }
    });

    loop {
        let (stream, peer) = tcp.accept().await?;
        let st = state.clone();
        tokio::spawn(async move {
            let _ = handle_tcp_conn(st, stream, peer).await;
        });
    }
}

async fn handle_tcp_conn(
    state: Arc<AppState>,
    mut stream: TcpStream,
    peer: SocketAddr,
) -> std::io::Result<()> {
    let mut len_buf = [0u8; 2];
    stream.read_exact(&mut len_buf).await?;
    let len = u16::from_be_bytes(len_buf) as usize;
    if len == 0 || len > 65535 {
        return Ok(());
    }
    let mut payload = vec![0u8; len];
    stream.read_exact(&mut payload).await?;
    if let Some(resp) = handle_query(state, payload, peer, true).await {
        let mut out = Vec::with_capacity(resp.len() + 2);
        out.extend_from_slice(&(resp.len() as u16).to_be_bytes());
        out.extend_from_slice(&resp);
        stream.write_all(&out).await?;
        stream.flush().await?;
    }
    Ok(())
}

async fn handle_query(
    state: Arc<AppState>,
    payload: Vec<u8>,
    peer: SocketAddr,
    via_tcp: bool,
) -> Option<Vec<u8>> {
    let started = Instant::now();
    let request = match Message::from_vec(&payload) {
        Ok(m) => m,
        Err(err) => {
            eprintln!(
                "[core] DNS 报文解析失败: {err} len={} head={:?}",
                payload.len(),
                &payload[..payload.len().min(16)]
            );
            return None;
        }
    };
    if request.message_type() != MessageType::Query || request.queries().is_empty() {
        eprintln!(
            "[core] 非查询报文: type={:?} qd={}",
            request.message_type(),
            request.queries().len()
        );
        return None;
    }
    let query = request.queries()[0].clone();
    let name = query.name().to_ascii().trim_end_matches('.').to_ascii_lowercase();
    let qtype = query.query_type();
    state.stats.total.fetch_add(1, Ordering::Relaxed);

    let (protection, mode) = {
        let cfg = state.cfg.read().unwrap();
        (cfg.protection_enabled, cfg.block_mode.clone())
    };

    if protection {
        let engine = state.engine.read().unwrap().clone();
        let verdict = engine.check(&name);
        if verdict.blocked {
            state.record_block(&name);
            state.push_log(LogEntry {
                time: now_text(),
                domain: name.clone(),
                qtype: format!("{qtype}"),
                action: "blocked".to_string(),
                rule: verdict.rule.clone(),
                client: format!("{}", peer.ip()),
                elapsed_ms: started.elapsed().as_millis() as u64,
            });
            return Some(build_block_response(&request, &mode));
        }
        if verdict.exception {
            // 命中白名单，直接放行（不再走缓存以外逻辑，但仍会转发）
            let _ = verdict;
        }
    }

    // 缓存
    let cache_key = format!("{}|{}", name, u16::from(qtype));
    if let Some(resp) = lookup_cache(&state, &cache_key, request.id()) {
        state.stats.cache_hits.fetch_add(1, Ordering::Relaxed);
        state.stats.allowed.fetch_add(1, Ordering::Relaxed);
        state.push_log(LogEntry {
            time: now_text(),
            domain: name,
            qtype: format!("{qtype}"),
            action: "cached".to_string(),
            rule: String::new(),
            client: format!("{}", peer.ip()),
            elapsed_ms: started.elapsed().as_millis() as u64,
        });
        return Some(resp);
    }

    match forward(&state, &payload, via_tcp).await {
        Some(mut response) => {
            response.set_id(request.id());
            store_cache(&state, &cache_key, response.clone());
            state.stats.allowed.fetch_add(1, Ordering::Relaxed);
            state.push_log(LogEntry {
                time: now_text(),
                domain: name,
                qtype: format!("{qtype}"),
                action: "allowed".to_string(),
                rule: String::new(),
                client: format!("{}", peer.ip()),
                elapsed_ms: started.elapsed().as_millis() as u64,
            });
            response.to_vec().ok()
        }
        None => {
            state.stats.upstream_errors.fetch_add(1, Ordering::Relaxed);
            state.push_log(LogEntry {
                time: now_text(),
                domain: name,
                qtype: format!("{qtype}"),
                action: "upstream-error".to_string(),
                rule: String::new(),
                client: format!("{}", peer.ip()),
                elapsed_ms: started.elapsed().as_millis() as u64,
            });
            Some(build_servfail(&request))
        }
    }
}

fn lookup_cache(state: &Arc<AppState>, key: &str, id: u16) -> Option<Vec<u8>> {
    let enabled = state.cfg.read().unwrap().cache_enabled;
    if !enabled {
        return None;
    }
    let mut cache = state.cache.lock().unwrap();
    let now = Instant::now();
    if let Some(entry) = cache.get(key) {
        if entry.expires > now {
            let mut msg = entry.msg.clone();
            msg.set_id(id);
            return msg.to_vec().ok();
        }
    }
    cache.remove(key);
    None
}

fn store_cache(state: &Arc<AppState>, key: &str, msg: Message) {
    let cfg = state.cfg.read().unwrap();
    if !cfg.cache_enabled {
        return;
    }
    let max = cfg.cache_size.max(64);
    drop(cfg);
    let ttl = msg
        .answers()
        .iter()
        .map(|r| r.ttl())
        .min()
        .unwrap_or(30)
        .clamp(10, 3600);
    let mut cache = state.cache.lock().unwrap();
    if cache.len() >= max {
        let now = Instant::now();
        cache.retain(|_, v| v.expires > now);
        if cache.len() >= max {
            cache.clear();
        }
    }
    cache.insert(
        key.to_string(),
        CacheEntry {
            msg,
            expires: Instant::now() + Duration::from_secs(ttl as u64),
        },
    );
}

async fn forward(state: &Arc<AppState>, payload: &[u8], prefer_tcp: bool) -> Option<Message> {
    let upstreams = state.cfg.read().unwrap().upstreams.clone();
    for upstream in upstreams {
        let addr: SocketAddr = match upstream.parse() {
            Ok(a) => a,
            Err(_) => continue,
        };
        let use_tcp = prefer_tcp;
        let result = if use_tcp {
            query_tcp(addr, payload).await
        } else {
            query_udp(addr, payload).await
        };
        match result {
            Some(msg) => {
                if !use_tcp && msg.truncated() {
                    if let Some(tcp_msg) = query_tcp(addr, payload).await {
                        return Some(tcp_msg);
                    }
                }
                return Some(msg);
            }
            None => continue,
        }
    }
    None
}

async fn query_udp(addr: SocketAddr, payload: &[u8]) -> Option<Message> {
    let sock = UdpSocket::bind("0.0.0.0:0").await.ok()?;
    sock.connect(addr).await.ok()?;
    if sock.send(payload).await.is_err() {
        return None;
    }
    let mut buf = vec![0u8; 4096];
    let fut = async {
        match sock.recv_from(&mut buf).await {
            Ok((len, _from)) => Message::from_vec(&buf[..len]).ok(),
            Err(_) => None,
        }
    };
    // 单个上游 UDP 超时 2s：局域网/路由器 DNS 正常都在几百毫秒内，
    // 超时太久会让后面的备用上游（公共 DNS）来不及在客户端超时前接管
    tokio::time::timeout(Duration::from_secs(2), fut)
        .await
        .ok()
        .flatten()
}

async fn query_tcp(addr: SocketAddr, payload: &[u8]) -> Option<Message> {
    let fut = async {
        let mut stream = TcpStream::connect(addr).await.ok()?;
        let mut out = Vec::with_capacity(payload.len() + 2);
        out.extend_from_slice(&(payload.len() as u16).to_be_bytes());
        out.extend_from_slice(payload);
        stream.write_all(&out).await.ok()?;
        stream.flush().await.ok()?;
        let mut len_buf = [0u8; 2];
        stream.read_exact(&mut len_buf).await.ok()?;
        let len = u16::from_be_bytes(len_buf) as usize;
        let mut buf = vec![0u8; len];
        stream.read_exact(&mut buf).await.ok()?;
        Message::from_vec(&buf).ok()
    };
    tokio::time::timeout(Duration::from_secs(6), fut)
        .await
        .ok()
        .flatten()
}

fn build_block_response(request: &Message, mode: &str) -> Vec<u8> {
    let mut msg = Message::new();
    msg.set_id(request.id());
    msg.set_message_type(MessageType::Response);
    msg.set_op_code(request.op_code());
    msg.set_recursion_desired(request.recursion_desired());
    msg.set_recursion_available(true);
    if let Some(q) = request.queries().first() {
        msg.add_query(q.clone());
        if mode == "zero" {
            let ttl = 30;
            match q.query_type() {
                RecordType::A => {
                    msg.add_answer(Record::from_rdata(
                        q.name().clone(),
                        ttl,
                        RData::A(A(Ipv4Addr::UNSPECIFIED)),
                    ));
                    msg.set_response_code(ResponseCode::NoError);
                }
                RecordType::AAAA => {
                    msg.add_answer(Record::from_rdata(
                        q.name().clone(),
                        ttl,
                        RData::AAAA(AAAA(Ipv6Addr::UNSPECIFIED)),
                    ));
                    msg.set_response_code(ResponseCode::NoError);
                }
                RecordType::HTTPS | RecordType::SVCB => {
                    // HTTPS 记录无法用 0.0.0.0 覆盖，返回空答案
                    msg.set_response_code(ResponseCode::NoError);
                }
                _ => {
                    msg.set_response_code(ResponseCode::NXDomain);
                }
            }
        } else {
            msg.set_response_code(ResponseCode::NXDomain);
        }
    } else {
        msg.set_response_code(ResponseCode::NXDomain);
    }
    msg.to_vec().unwrap_or_default()
}

fn build_servfail(request: &Message) -> Vec<u8> {
    let mut msg = Message::new();
    msg.set_id(request.id());
    msg.set_message_type(MessageType::Response);
    msg.set_op_code(request.op_code());
    msg.set_recursion_desired(request.recursion_desired());
    msg.set_recursion_available(true);
    if let Some(q) = request.queries().first() {
        msg.add_query(q.clone());
    }
    msg.set_response_code(ResponseCode::ServFail);
    msg.to_vec().unwrap_or_default()
}
