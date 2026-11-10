package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type FilterConfig struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	URL                   string  `json:"url"`
	Enabled               bool    `json:"enabled"`
	AutoUpdate            bool    `json:"auto_update"`
	UpdateIntervalMinutes int     `json:"update_interval_minutes"`
	LastUpdate            *string `json:"last_update"`
	LastUpdateTS          int64   `json:"last_update_ts"`
	LastError             *string `json:"last_error"`
	RuleCount             int     `json:"rule_count"`
	Builtin               bool    `json:"builtin"`
	Kind                  string  `json:"kind"`
}

type Config struct {
	ListenAddress     string         `json:"listen_address"`
	ListenPort        int            `json:"listen_port"`
	ControlPort       int            `json:"control_port"`
	ProtectionEnabled bool           `json:"protection_enabled"`
	BlockMode         string         `json:"block_mode"`
	Upstreams         []string       `json:"upstreams"`
	CacheSize         int            `json:"cache_size"`
	CacheEnabled      bool           `json:"cache_enabled"`
	LogQueries        bool           `json:"log_queries"`
	MaxLogEntries     int            `json:"max_log_entries"`
	Filters           []FilterConfig `json:"filters"`
	CustomRules       []string       `json:"custom_rules"`
	DataDir           *string        `json:"data_dir"`
	PanelPort         int            `json:"panel_port"`
	DNSMode           string         `json:"dns_mode"`
	AutoStart         bool           `json:"auto_start"`
	// UpstreamMode: "custom" = 用 CustomUpstreams；"system" = 每次启动/接管前抓本机 DNS 当上游
	UpstreamMode       string   `json:"upstream_mode"`
	CustomUpstreams    []string `json:"custom_upstreams"`
	UpstreamSource     string   `json:"upstream_source"`
	UpstreamCapturedAt string   `json:"upstream_captured_at"`
}

const defaultSubscription = "https://fastly.jsdelivr.net/gh/abclq/home-dns-adblock@main/dist/adguard-dns-full.txt"

func DataDir() string {
	if dir := os.Getenv("SHIELDDNS_DATA_DIR"); dir != "" {
		return dir
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base, _ = os.UserHomeDir()
	}
	return filepath.Join(base, "ShieldDNS")
}

func ConfigPath() string {
	return filepath.Join(DataDir(), "config.json")
}

func defaultUpstreams() []string {
	return []string{"223.5.5.5:53", "119.29.29.29:53", "1.1.1.1:53"}
}

func defaultConfig() *Config {
	return &Config{
		ListenAddress:     "127.0.0.1",
		ListenPort:        53,
		ControlPort:       5399,
		ProtectionEnabled: false,
		BlockMode:         "nxdomain",
		Upstreams:         defaultUpstreams(),
		UpstreamMode:      "custom",
		CustomUpstreams:   defaultUpstreams(),
		CacheSize:         4096,
		CacheEnabled:      true,
		LogQueries:        true,
		MaxLogEntries:     500,
		PanelPort:         8899,
		DNSMode:           "custom",
		AutoStart:         true,
		Filters: []FilterConfig{{
			ID:                    "abclq",
			Name:                  "home-dns-adblock（红果/番茄等国内 App）",
			URL:                   defaultSubscription,
			Enabled:               true,
			AutoUpdate:            true,
			UpdateIntervalMinutes: 720,
			Builtin:               true,
			Kind:                  "subscription",
		}},
		CustomRules: []string{
			"! 自定义规则示例（AdGuard 语法，一行一条）",
			"! ||ads.example.com^",
		},
	}
}

func loadConfig() *Config {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return defaultConfig()
	}
	cfg := defaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return defaultConfig()
	}
	if cfg.PanelPort == 0 {
		cfg.PanelPort = 8899
	}
	if cfg.DNSMode == "" {
		cfg.DNSMode = "custom"
	}
	if cfg.UpstreamMode != "system" {
		cfg.UpstreamMode = "custom"
	}
	if len(cfg.CustomUpstreams) == 0 {
		cfg.CustomUpstreams = append([]string{}, cfg.Upstreams...)
	}
	if cfg.UpstreamMode == "custom" && len(cfg.Upstreams) == 0 {
		cfg.Upstreams = append([]string{}, cfg.CustomUpstreams...)
	}
	return cfg
}

func saveConfig(cfg *Config) error {
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := ConfigPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	_ = os.Remove(ConfigPath())
	return os.Rename(tmp, ConfigPath())
}
