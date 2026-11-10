use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};

// 发行版不内置任何第三方规则订阅：规则由用户自行在面板里添加，
// 本程序只负责下载、解析、匹配。

fn default_true() -> bool {
    true
}
fn default_listen() -> String {
    "127.0.0.1".to_string()
}
fn default_port() -> u16 {
    // DNS 只有 53 才能被系统解析器命中（Windows 网卡 DNS 不能带端口），
    // 所以默认就是 53；绑不上直接失败，由面板提示占用者，不做静默回退。
    53
}
fn default_control_port() -> u16 {
    5399
}
fn default_block_mode() -> String {
    "nxdomain".to_string()
}
fn default_upstreams() -> Vec<String> {
    vec![
        "223.5.5.5:53".to_string(),
        "119.29.29.29:53".to_string(),
        "1.1.1.1:53".to_string(),
    ]
}
fn default_interval() -> u64 {
    720
}
fn default_cache_size() -> usize {
    4096
}
fn default_log_max() -> usize {
    500
}
fn default_panel_port() -> u16 {
    8899
}
fn default_dns_mode() -> String {
    "custom".to_string()
}
fn default_upstream_mode() -> String {
    "custom".to_string()
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct FilterConfig {
    pub id: String,
    pub name: String,
    pub url: String,
    #[serde(default = "default_true")]
    pub enabled: bool,
    #[serde(default = "default_true")]
    pub auto_update: bool,
    #[serde(default = "default_interval")]
    pub update_interval_minutes: u64,
    #[serde(default)]
    pub last_update: Option<String>,
    #[serde(default)]
    pub last_update_ts: u64,
    #[serde(default)]
    pub last_error: Option<String>,
    #[serde(default)]
    pub rule_count: usize,
    #[serde(default)]
    pub builtin: bool,
    /// "subscription" | "local"
    #[serde(default = "default_kind")]
    pub kind: String,
}

fn default_kind() -> String {
    "subscription".to_string()
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct Config {
    #[serde(default = "default_listen")]
    pub listen_address: String,
    #[serde(default = "default_port")]
    pub listen_port: u16,
    #[serde(default = "default_control_port")]
    pub control_port: u16,
    #[serde(default = "default_true")]
    pub protection_enabled: bool,
    #[serde(default = "default_block_mode")]
    pub block_mode: String,
    #[serde(default = "default_upstreams")]
    pub upstreams: Vec<String>,
    #[serde(default = "default_cache_size")]
    pub cache_size: usize,
    #[serde(default = "default_true")]
    pub cache_enabled: bool,
    #[serde(default = "default_true")]
    pub log_queries: bool,
    #[serde(default = "default_log_max")]
    pub max_log_entries: usize,
    #[serde(default)]
    pub filters: Vec<FilterConfig>,
    #[serde(default)]
    pub custom_rules: Vec<String>,
    #[serde(default)]
    pub data_dir: Option<String>,
    // 以下字段由 Go 面板使用，核心只负责原样保存
    #[serde(default = "default_panel_port")]
    pub panel_port: u16,
    #[serde(default = "default_dns_mode")]
    pub dns_mode: String,
    #[serde(default = "default_true")]
    pub auto_start: bool,
    /// "custom" = 用 custom_upstreams；"system" = 面板在开启前抓本机 DNS 写进 upstreams
    #[serde(default = "default_upstream_mode")]
    pub upstream_mode: String,
    #[serde(default)]
    pub custom_upstreams: Vec<String>,
    #[serde(default)]
    pub upstream_source: String,
    #[serde(default)]
    pub upstream_captured_at: String,
}

impl Default for Config {
    fn default() -> Self {
        Config {
            listen_address: default_listen(),
            listen_port: default_port(),
            control_port: default_control_port(),
            protection_enabled: true,
            block_mode: default_block_mode(),
            upstreams: default_upstreams(),
            cache_size: default_cache_size(),
            cache_enabled: true,
            log_queries: true,
            max_log_entries: default_log_max(),
            filters: Vec::new(),
            custom_rules: vec![
                "! 自定义规则示例（AdGuard 语法，一行一条）".to_string(),
                "! ||ads.example.com^".to_string(),
            ],
            data_dir: None,
            panel_port: default_panel_port(),
            dns_mode: default_dns_mode(),
            auto_start: true,
            upstream_mode: default_upstream_mode(),
            custom_upstreams: default_upstreams(),
            upstream_source: String::new(),
            upstream_captured_at: String::new(),
        }
    }
}

impl Config {
    pub fn data_path(&self, config_path: &Path) -> PathBuf {
        if let Some(dir) = &self.data_dir {
            PathBuf::from(dir)
        } else {
            config_path
                .parent()
                .map(|p| p.to_path_buf())
                .unwrap_or_else(|| PathBuf::from("."))
        }
    }

    pub fn filters_dir(&self, config_path: &Path) -> PathBuf {
        self.data_path(config_path).join("filters")
    }
}

pub fn load_or_create(path: &Path) -> Config {
    match std::fs::read_to_string(path) {
        Ok(text) => match serde_json::from_str::<Config>(&text) {
            Ok(cfg) => cfg,
            Err(err) => {
                eprintln!("[core] config 解析失败（使用默认配置）: {err}");
                let cfg = Config::default();
                save(path, &cfg);
                cfg
            }
        },
        Err(_) => {
            let cfg = Config::default();
            save(path, &cfg);
            cfg
        }
    }
}

pub fn save(path: &Path, cfg: &Config) {
    let tmp = path.with_extension("json.tmp");
    if let Ok(text) = serde_json::to_string_pretty(cfg) {
        if std::fs::write(&tmp, text).is_ok() {
            // Windows 上 rename 目标已存在会失败，先删再改
            let _ = std::fs::remove_file(path);
            if let Err(err) = std::fs::rename(&tmp, path) {
                eprintln!("[core] 写入配置失败: {err}");
            }
        }
    }
}
