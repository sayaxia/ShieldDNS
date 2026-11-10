mod api;
mod config;
mod dns;
mod engine;
mod state;
mod updater;

use std::path::PathBuf;

fn default_config_path() -> PathBuf {
    if let Ok(dir) = std::env::var("SHIELDDNS_DATA_DIR") {
        return PathBuf::from(dir).join("config.json");
    }
    if let Ok(local) = std::env::var("LOCALAPPDATA") {
        return PathBuf::from(local).join("ShieldDNS").join("config.json");
    }
    PathBuf::from("config.json")
}

#[tokio::main]
async fn main() {
    let mut config_path = default_config_path();
    let args: Vec<String> = std::env::args().collect();
    let mut i = 1;
    while i < args.len() {
        match args[i].as_str() {
            "--config" | "-c" => {
                if i + 1 < args.len() {
                    config_path = PathBuf::from(&args[i + 1]);
                    i += 2;
                } else {
                    i += 1;
                }
            }
            "--version" | "-v" => {
                println!("shielddns-core {}", env!("CARGO_PKG_VERSION"));
                return;
            }
            _ => i += 1,
        }
    }

    if let Some(parent) = config_path.parent() {
        let _ = std::fs::create_dir_all(parent);
    }
    let cfg = config::load_or_create(&config_path);
    let data_dir = cfg.data_path(&config_path);
    let _ = std::fs::create_dir_all(data_dir.join("filters"));

    println!("[core] ShieldDNS core {} 启动", env!("CARGO_PKG_VERSION"));
    println!("[core] 配置文件: {}", config_path.display());
    println!("[core] 监听端口: {}:{}", cfg.listen_address, cfg.listen_port);

    let state = state::AppState::new(config_path.clone(), cfg);
    state.reload_engine();
    {
        let cfg = state.cfg.read().unwrap();
        let dir = cfg.filters_dir(&state.config_path);
        let missing: Vec<String> = cfg
            .filters
            .iter()
            .filter(|f| f.enabled && !dir.join(format!("{}.txt", f.id)).exists())
            .map(|f| f.id.clone())
            .collect();
        drop(cfg);
        if !missing.is_empty() {
            let st = state.clone();
            tokio::spawn(async move {
                for id in missing {
                    match updater::update_filter(st.clone(), &id).await {
                        Ok(n) => println!("[core] 首次下载 {id}: {n} 条"),
                        Err(err) => {
                            eprintln!("[core] 首次下载 {id} 失败: {err}");
                            updater::mark_error(&st, &id, &err);
                        }
                    }
                }
            });
        }
    }

    let dns_state = state.clone();
    tokio::spawn(async move {
        if let Err(err) = dns::run(dns_state).await {
            eprintln!("[core] DNS 服务启动失败: {err}");
            std::process::exit(2);
        }
    });

    let api_state = state.clone();
    tokio::spawn(async move {
        if let Err(err) = api::run(api_state).await {
            eprintln!("[core] 控制接口启动失败: {err}");
            std::process::exit(3);
        }
    });

    tokio::spawn(updater::auto_update_loop(state.clone()));

    loop {
        tokio::time::sleep(std::time::Duration::from_secs(3600)).await;
    }
}
