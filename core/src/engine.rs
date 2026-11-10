use regex::Regex;
use std::collections::HashMap;
use std::net::IpAddr;

#[derive(Debug, Clone)]
pub struct CheckResult {
    pub blocked: bool,
    pub exception: bool,
    pub rule: String,
}

impl CheckResult {
    pub fn allowed() -> Self {
        CheckResult {
            blocked: false,
            exception: false,
            rule: String::new(),
        }
    }
}

pub struct Engine {
    block_exact: HashMap<String, String>,
    block_suffix: HashMap<String, String>,
    ex_exact: HashMap<String, String>,
    ex_suffix: HashMap<String, String>,
    block_regex: Vec<(Regex, String)>,
    ex_regex: Vec<(Regex, String)>,
    pub parsed_rules: usize,
    pub block_rules: usize,
    pub exception_rules: usize,
    pub regex_rules: usize,
}

impl Default for Engine {
    fn default() -> Self {
        Engine::new()
    }
}

impl Engine {
    pub fn new() -> Self {
        Engine {
            block_exact: HashMap::new(),
            block_suffix: HashMap::new(),
            ex_exact: HashMap::new(),
            ex_suffix: HashMap::new(),
            block_regex: Vec::new(),
            ex_regex: Vec::new(),
            parsed_rules: 0,
            block_rules: 0,
            exception_rules: 0,
            regex_rules: 0,
        }
    }

    /// 构建引擎：filters 为 (规则文本, 是否启用)，custom 为用户自定义规则
    pub fn build(filters: &[(String, bool)], custom: &[String]) -> Engine {
        let mut engine = Engine::new();
        for (text, enabled) in filters {
            if !enabled {
                continue;
            }
            for line in text.lines() {
                engine.add_rule(line);
            }
        }
        for line in custom {
            engine.add_rule(line);
        }
        engine.block_rules = engine.block_exact.len() + engine.block_suffix.len() + engine.block_regex.len();
        engine.exception_rules =
            engine.ex_exact.len() + engine.ex_suffix.len() + engine.ex_regex.len();
        engine
    }

    pub fn check(&self, name: &str) -> CheckResult {
        let name = name.trim_end_matches('.').to_ascii_lowercase();
        if name.is_empty() {
            return CheckResult::allowed();
        }
        if let Some(rule) = match_map(&self.ex_exact, &self.ex_suffix, &name) {
            return CheckResult {
                blocked: false,
                exception: true,
                rule,
            };
        }
        if let Some(rule) = self.match_regex(&self.ex_regex, &name) {
            return CheckResult {
                blocked: false,
                exception: true,
                rule,
            };
        }
        if let Some(rule) = match_map(&self.block_exact, &self.block_suffix, &name) {
            return CheckResult {
                blocked: true,
                exception: false,
                rule,
            };
        }
        if let Some(rule) = self.match_regex(&self.block_regex, &name) {
            return CheckResult {
                blocked: true,
                exception: false,
                rule,
            };
        }
        CheckResult::allowed()
    }

    fn match_regex(&self, list: &[(Regex, String)], name: &str) -> Option<String> {
        for (re, rule) in list {
            if re.is_match(name) {
                return Some(rule.clone());
            }
        }
        None
    }

    pub fn add_rule(&mut self, raw: &str) {
        let mut line = raw.trim().to_string();
        if line.is_empty() || line.starts_with('!') || line.starts_with('[') || line.starts_with('#')
        {
            return;
        }
        if let Some(pos) = line.find(" #") {
            line.truncate(pos);
            line = line.trim().to_string();
        }
        if line.is_empty() {
            return;
        }

        let mut exception = false;
        if let Some(rest) = line.strip_prefix("@@") {
            exception = true;
            line = rest.trim().to_string();
        }
        if line.is_empty() {
            return;
        }

        // 正则规则 /pattern/
        if line.starts_with('/') {
            if let Some(end) = line.rfind('/') {
                if end > 1 {
                    let pat = &line[1..end];
                    if let Ok(re) = Regex::new(&format!("(?i){pat}")) {
                        self.push_regex(re, raw.trim().to_string(), exception);
                        self.parsed_rules += 1;
                        return;
                    }
                }
            }
        }

        // hosts 格式：0.0.0.0 domain.com
        let parts: Vec<&str> = line.split_whitespace().collect();
        if parts.len() >= 2 {
            if let Ok(_ip) = parts[0].parse::<IpAddr>() {
                let domain = normalize_domain(parts[1]);
                if !domain.is_empty() && domain.contains('.') {
                    self.push_suffix(domain, raw.trim().to_string(), exception);
                    self.parsed_rules += 1;
                }
                return;
            }
        }

        // 去掉 $modifier
        if let Some(pos) = line.rfind('$') {
            line.truncate(pos);
        }
        let line = line.trim();
        if line.is_empty() {
            return;
        }

        // |http://...| 形式，尽力提取主机名
        if line.starts_with('|') && line.contains("://") {
            if let Some(host) = extract_host(line) {
                if !host.is_empty() {
                    self.push_suffix(host, raw.trim().to_string(), exception);
                    self.parsed_rules += 1;
                }
            }
            return;
        }

        let mut pattern = line;
        let mut prefix_anchor = false;
        if let Some(rest) = pattern.strip_prefix("||") {
            pattern = rest;
        } else if let Some(rest) = pattern.strip_prefix('|') {
            pattern = rest;
            prefix_anchor = true;
        }
        let pattern = pattern.trim_start_matches('|').trim_end_matches('|');
        let pattern = pattern.split('^').next().unwrap_or(pattern);
        let pattern = pattern.split('/').next().unwrap_or(pattern);
        let pattern = pattern.trim().trim_matches('.');
        if pattern.is_empty() {
            return;
        }

        if pattern.contains('*') || pattern.contains('?') {
            if let Some(re) = compile_wildcard(pattern, !prefix_anchor) {
                self.push_regex(re, raw.trim().to_string(), exception);
                self.parsed_rules += 1;
            }
            return;
        }

        let domain = normalize_domain(pattern);
        if domain.is_empty() {
            return;
        }
        // 只接受像域名的规则，避免误伤单段关键字
        if !domain.contains('.') {
            return;
        }
        if prefix_anchor {
            self.push_exact_or_suffix(domain, raw.trim().to_string(), exception, false);
        } else {
            self.push_suffix(domain, raw.trim().to_string(), exception);
        }
        self.parsed_rules += 1;
    }

    fn push_exact_or_suffix(&mut self, domain: String, rule: String, exception: bool, suffix: bool) {
        let map = match (exception, suffix) {
            (true, false) => &mut self.ex_exact,
            (true, true) => &mut self.ex_suffix,
            (false, false) => &mut self.block_exact,
            (false, true) => &mut self.block_suffix,
        };
        map.entry(domain).or_insert(rule);
    }

    fn push_suffix(&mut self, domain: String, rule: String, exception: bool) {
        self.push_exact_or_suffix(domain, rule, exception, true);
    }

    fn push_regex(&mut self, re: Regex, rule: String, exception: bool) {
        if exception {
            self.ex_regex.push((re, rule));
        } else {
            self.block_regex.push((re, rule));
        }
        self.regex_rules += 1;
    }
}

fn match_map(
    exact: &HashMap<String, String>,
    suffix: &HashMap<String, String>,
    name: &str,
) -> Option<String> {
    if let Some(rule) = exact.get(name) {
        return Some(rule.clone());
    }
    let mut cur = name;
    loop {
        if let Some(rule) = suffix.get(cur) {
            return Some(rule.clone());
        }
        match cur.find('.') {
            Some(idx) if idx + 1 < cur.len() => cur = &cur[idx + 1..],
            _ => return None,
        }
    }
}

fn normalize_domain(raw: &str) -> String {
    let d = raw
        .trim()
        .trim_start_matches('*')
        .trim_start_matches('.')
        .trim_end_matches('.')
        .to_ascii_lowercase();
    if d.contains(' ') || d.contains('/') || d.contains(':') {
        return String::new();
    }
    d
}

fn extract_host(line: &str) -> Option<String> {
    let s = line.trim_start_matches('|');
    let after = s.split("://").nth(1)?;
    let host = after
        .split(['/', '^', '|', ':'])
        .next()
        .unwrap_or("")
        .trim();
    let host = host.strip_prefix("www.").unwrap_or(host);
    Some(normalize_domain(host))
}

fn compile_wildcard(pattern: &str, allow_subdomain: bool) -> Option<Regex> {
    let body = regex::escape(pattern).replace("\\*", "[a-z0-9._-]*");
    let prefix = if allow_subdomain {
        "^([a-z0-9._-]+\\.)*"
    } else {
        "^"
    };
    Regex::new(&format!("{prefix}{body}$")).ok()
}
