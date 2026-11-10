use crate::config::{save, Config};
use crate::engine::Engine;
use hickory_proto::op::Message;
use serde::Serialize;
use std::collections::{HashMap, VecDeque};
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, RwLock};
use std::time::{Instant, SystemTime, UNIX_EPOCH};

pub struct CacheEntry {
    pub msg: Message,
    pub expires: Instant,
}

#[derive(Serialize, Clone, Debug)]
pub struct LogEntry {
    pub time: String,
    pub domain: String,
    pub qtype: String,
    pub action: String,
    pub rule: String,
    pub client: String,
    pub elapsed_ms: u64,
}

pub struct Stats {
    pub total: AtomicU64,
    pub blocked: AtomicU64,
    pub allowed: AtomicU64,
    pub cache_hits: AtomicU64,
    pub upstream_errors: AtomicU64,
    pub started: Instant,
    pub blocked_domains: Mutex<HashMap<String, u64>>,
}

impl Stats {
    fn new() -> Self {
        Stats {
            total: AtomicU64::new(0),
            blocked: AtomicU64::new(0),
            allowed: AtomicU64::new(0),
            cache_hits: AtomicU64::new(0),
            upstream_errors: AtomicU64::new(0),
            started: Instant::now(),
            blocked_domains: Mutex::new(HashMap::new()),
        }
    }

    pub fn uptime_seconds(&self) -> u64 {
        self.started.elapsed().as_secs()
    }
}

pub struct AppState {
    pub config_path: PathBuf,
    pub cfg: RwLock<Config>,
    pub engine: RwLock<Arc<Engine>>,
    pub stats: Stats,
    pub log: Mutex<VecDeque<LogEntry>>,
    pub cache: Mutex<HashMap<String, CacheEntry>>,
    pub client: reqwest::Client,
    pub effective_rules: AtomicU64,
}

impl AppState {
    pub fn new(config_path: PathBuf, cfg: Config) -> Arc<Self> {
        let client = reqwest::Client::builder()
            .user_agent("ShieldDNS/0.1 (+https://github.com/sayaxia/ShieldDNS)")
            .timeout(std::time::Duration::from_secs(30))
            .build()
            .unwrap_or_default();
        let engine = Engine::new();
        let state = Arc::new(AppState {
            config_path,
            cfg: RwLock::new(cfg),
            engine: RwLock::new(Arc::new(engine)),
            stats: Stats::new(),
            log: Mutex::new(VecDeque::new()),
            cache: Mutex::new(HashMap::new()),
            client,
            effective_rules: AtomicU64::new(0),
        });
        state
    }

    pub fn reload_engine(&self) {
        let cfg = self.cfg.read().unwrap().clone();
        let dir = cfg.filters_dir(&self.config_path);
        let mut texts: Vec<(String, bool)> = Vec::new();
        for f in &cfg.filters {
            let path = dir.join(format!("{}.txt", f.id));
            let text = std::fs::read_to_string(&path).unwrap_or_default();
            texts.push((text, f.enabled));
        }
        let engine = Engine::build(&texts, &cfg.custom_rules);
        self.effective_rules
            .store(engine.parsed_rules as u64, Ordering::Relaxed);
        *self.engine.write().unwrap() = Arc::new(engine);
        println!("[core] 过滤器已重建");
    }

    pub fn save_config(&self) {
        let cfg = self.cfg.read().unwrap().clone();
        save(&self.config_path, &cfg);
    }

    pub fn push_log(&self, entry: LogEntry) {
        let cfg = self.cfg.read().unwrap();
        if !cfg.log_queries {
            return;
        }
        let max = cfg.max_log_entries.max(50);
        drop(cfg);
        let mut log = self.log.lock().unwrap();
        log.push_front(entry);
        while log.len() > max {
            log.pop_back();
        }
    }

    pub fn record_block(&self, domain: &str) {
        self.stats.blocked.fetch_add(1, Ordering::Relaxed);
        let mut map = self.stats.blocked_domains.lock().unwrap();
        *map.entry(domain.to_string()).or_insert(0) += 1;
    }

    pub fn clear_cache(&self) {
        self.cache.lock().unwrap().clear();
    }

    pub fn prune_cache(&self) {
        let now = Instant::now();
        let mut cache = self.cache.lock().unwrap();
        cache.retain(|_, v| v.expires > now);
    }
}

pub fn now_text() -> String {
    format_ts(unix_now())
}

pub fn unix_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

pub fn format_ts(secs: u64) -> String {
    // 不引入 chrono，手工把秒转换为本地可读的 UTC+8 时间
    let local = secs + 8 * 3600;
    let day = local / 86400;
    let tod = local % 86400;
    let (y, m, d) = civil_from_days(day as i64);
    format!(
        "{:04}-{:02}-{:02} {:02}:{:02}:{:02}",
        y,
        m,
        d,
        tod / 3600,
        (tod % 3600) / 60,
        tod % 60
    )
}

fn civil_from_days(z: i64) -> (i32, u32, u32) {
    let z = z + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = (z - era * 146097) as u64;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    ((y + if m <= 2 { 1 } else { 0 }) as i32, m, d)
}
