const state = {
  page: "overview",
  status: null,
  info: null,
  filters: [],
  logs: [],
  rulesLoaded: false,
  toggling: false,
  tick: 0,
  // 设置页有未保存的修改时，轮询不能拿服务器旧值回写，否则用户刚点的「53 端口 + 自动接管」会被打回 25353
  settingsDirty: false,
};

const PAGE_META = {
  overview: ["概览", "实时查看防护状态与拦截效果"],
  filters: ["过滤订阅", "通过 URL 订阅 AdGuard 规则并自动更新"],
  rules: ["自定义规则", "手工维护的规则，保存后立即生效"],
  logs: ["查询日志", "每一次经过 ShieldDNS 的域名查询"],
  settings: ["设置", "端口、上游、系统 DNS 接管与运行环境"],
};

const $ = (id) => document.getElementById(id);
const esc = (s) =>
  String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");

function num(n) {
  return Number(n || 0).toLocaleString("zh-CN");
}

function duration(sec) {
  sec = Math.max(0, Number(sec || 0));
  if (sec < 60) return `${Math.floor(sec)} 秒`;
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟`;
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时 ${Math.floor((sec % 3600) / 60)} 分`;
  return `${Math.floor(sec / 86400)} 天 ${Math.floor((sec % 86400) / 3600)} 小时`;
}

function toast(text, type = "ok") {
  const box = $("toasts");
  const el = document.createElement("div");
  el.className = `toast ${type}`;
  el.textContent = text;
  box.appendChild(el);
  setTimeout(() => {
    el.style.opacity = "0";
    el.style.transform = "translateY(8px)";
    el.style.transition = "all .25s";
    setTimeout(() => el.remove(), 260);
  }, type === "err" ? 5200 : 3000);
}

async function api(path, opts = {}) {
  const init = { method: opts.method || "GET", cache: "no-store" };
  if (opts.body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(opts.body);
  }
  const res = await fetch(path, init);
  const text = await res.text();
  let data = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch (e) {
    data = { raw: text };
  }
  if (!res.ok) {
    throw new Error(data.error || data.message || `请求失败（HTTP ${res.status}）`);
  }
  return data;
}

function setText(id, value) {
  const el = $(id);
  if (el) el.textContent = value;
}

/* ---------- 概览 ---------- */

function renderStatus(st) {
  if (!st) {
    setText("sideState", "核心未连接");
    $("sideDot").className = "dot";
    return;
  }
  const on = !!st.protection;
  $("protectToggle").checked = on;
  $("hero").classList.toggle("paused", !on);
  $("heroIcon").src = on ? "/assets/shield_on.png" : "/assets/shield_off.png";
  $("statePill").className = `pill ${on ? "on" : "off"}`;
  $("statePill").innerHTML = `<i class="dot ${on ? "on" : "off"}"></i><span>${on ? "保护已开启" : "保护已暂停"}</span>`;
  $("heroTitle").textContent = on ? "DNS 防护正在运行" : "保护已暂停";
  $("heroDesc").textContent = on
    ? "所有域名查询会先经过过滤引擎，命中规则直接返回 NXDOMAIN，广告与追踪域名不会被真正解析。"
    : "过滤已关闭，所有查询都会直接转发给上游 DNS。重新打开开关即可恢复拦截。";
  $("switchLabel").textContent = on ? "已开启" : "已暂停";
  $("heroTime").textContent = st.time || "";

  $("heroListen").textContent = `${st.listen_address}:${st.listen_port}`;
  $("heroRules").textContent = num(st.rules_total);
  $("heroRate").textContent = `${(Number(st.blocked_percent || 0)).toFixed(1)}%`;

  const q = st.queries || {};
  setText("statTotal", num(q.total));
  setText("statBlocked", num(q.blocked));
  setText("statRate", `${(Number(st.blocked_percent || 0)).toFixed(1)}%`);
  setText("statCache", num(q.cache_hits));
  setText("statCacheSize", `缓存 ${num(st.cache_size)} 条`);
  setText("statRules", num(st.rules_total));
  setText("statCustom", `自定义 ${num(st.custom_rules)} 条`);
  setText("statUptime", duration(st.uptime_seconds));
  setText("statUpstreams", `上游 ${(st.upstreams || []).length} 个`);

  setText("sideState", on ? "保护已开启" : "保护已暂停");
  $("sideDot").className = `dot ${on ? "on" : "off"}`;
  setText("sidePort", `${st.listen_address}:${st.listen_port}`);

  $("chipPort").textContent = `${st.listen_address}:${st.listen_port}`;
  const chip = $("chipCore");
  chip.classList.remove("bad");
  chip.innerHTML = `<i class="dot on"></i><span>核心已连接</span>`;
}

function renderCoreDown() {
  const chip = $("chipCore");
  chip.classList.add("bad");
  chip.innerHTML = `<i class="dot" style="background:#ff7a7a"></i><span>核心未运行</span>`;
  setText("sideState", "核心未连接");
  $("sideDot").className = "dot";
}

function renderRecent(logs) {
  const box = $("recentLogs");
  if (!logs || !logs.length) {
    box.innerHTML = '<div class="empty small">还没有查询记录</div>';
    return;
  }
  box.innerHTML = logs
    .slice(0, 7)
    .map((l) => {
      const action = l.action || "allowed";
      const cls = action === "blocked" ? "blocked" : action === "cached" ? "cached" : "allowed";
      const label = action === "blocked" ? "拦截" : action === "cached" ? "缓存" : "放行";
      return `<div class="mini-row">
        <time>${esc((l.time || "").slice(11))}</time>
        <span class="domain" title="${esc(l.domain)}">${esc(l.domain)}</span>
        <span class="tag ${cls}">${label}</span>
      </div>`;
    })
    .join("");
}

function renderTop(list) {
  const box = $("topList");
  if (!list || !list.length) {
    box.innerHTML = '<div class="empty small">暂无拦截</div>';
    return;
  }
  const max = Math.max(...list.map((x) => Number(x.count) || 1));
  box.innerHTML = list
    .slice(0, 10)
    .map((x) => {
      const pct = Math.max(4, Math.round((Number(x.count) / max) * 100));
      return `<div class="top-row">
        <div class="bar-wrap"><span class="bar" style="width:${pct}%"></span><span class="name">${esc(x.domain)}</span></div>
        <span class="count">${num(x.count)}</span>
      </div>`;
    })
    .join("");
}

/* ---------- 过滤器 ---------- */

function filterInterval(min) {
  if (!min) return "—";
  if (min % 60 === 0) return `${min / 60} 小时`;
  return `${min} 分钟`;
}

function renderFilters() {
  const box = $("filterList");
  if (!state.filters.length) {
    box.innerHTML = '<div class="empty small">还没有过滤器，添加一个订阅地址试试。</div>';
    setText("filterSummary", "共 0 个");
    return;
  }
  const enabled = state.filters.filter((f) => f.enabled).length;
  const total = state.filters.reduce((s, f) => s + (f.rule_count || 0), 0);
  setText("filterSummary", `${state.filters.length} 个订阅 · 启用 ${enabled} 个 · 共 ${num(total)} 条规则`);

  box.innerHTML = state.filters
    .map((f) => {
      const meta = [
        `<span>规则 <b>${num(f.rule_count)}</b></span>`,
        `<span>更新 <b>${esc(f.last_update || "从未")}</b></span>`,
        `<span>自动 <b>${f.auto_update ? filterInterval(f.update_interval_minutes) : "关闭"}</b></span>`,
      ];
      if (!f.cached) meta.push('<span class="err">尚未下载</span>');
      if (f.last_error) meta.push(`<span class="err">${esc(f.last_error)}</span>`);
      return `<div class="filter-item" data-id="${esc(f.id)}">
        <div>
          <div class="f-name">${esc(f.name)}${f.builtin ? '<span class="badge ok">内置</span>' : ""}</div>
          <div class="f-url">${esc(f.url)}</div>
          <div class="f-meta">${meta.join("")}</div>
        </div>
        <div class="f-actions">
          <div class="f-toggle">
            <label class="switch mini-switch"><input type="checkbox" data-act="enable" ${f.enabled ? "checked" : ""}><span class="track"><span class="knob"></span></span></label>
            <span class="muted">启用</span>
          </div>
          <button class="ghost-btn" data-act="update">立即更新</button>
          <button class="ghost-btn" data-act="delete">删除</button>
        </div>
      </div>`;
    })
    .join("");
}

async function loadFilters() {
  try {
    const data = await api("/api/filters");
    state.filters = data.filters || [];
    renderFilters();
  } catch (e) {
    /* 核心未启动时静默 */
  }
}

async function updateAll() {
  try {
    await api("/api/filters/update", { method: "POST", body: {} });
    toast("已开始更新全部订阅，规则数稍后刷新");
    setTimeout(loadFilters, 2500);
  } catch (e) {
    toast(e.message, "err");
  }
}

async function addFilter() {
  const url = $("newFilterUrl").value.trim();
  const name = $("newFilterName").value.trim();
  if (!url) {
    toast("请填写过滤器 URL", "warn");
    return;
  }
  try {
    const res = await api("/api/filters", { method: "POST", body: { name, url } });
    if (res.existing) toast("该地址已在订阅列表中", "warn");
    else toast("已添加，正在后台下载规则");
    $("newFilterUrl").value = "";
    $("newFilterName").value = "";
    loadFilters();
  } catch (e) {
    toast(e.message, "err");
  }
}

async function filterAction(id, action, checked) {
  const f = state.filters.find((x) => x.id === id);
  if (!f) return;
  try {
    if (action === "enable") {
      await api(`/api/filters/${encodeURIComponent(id)}`, { method: "PATCH", body: { enabled: checked } });
      toast(checked ? "已启用该过滤器" : "已停用该过滤器");
      loadFilters();
    } else if (action === "update") {
      await api(`/api/filters/${encodeURIComponent(id)}/update`, { method: "POST", body: {} });
      toast("已开始更新，稍后查看规则数");
      setTimeout(loadFilters, 2500);
    } else if (action === "delete") {
      if (!confirm(`删除过滤器「${f.name}」？`)) return;
      await api(`/api/filters/${encodeURIComponent(id)}`, { method: "DELETE" });
      toast("已删除");
      loadFilters();
    }
  } catch (e) {
    toast(e.message, "err");
  }
}

/* ---------- 规则 ---------- */

async function loadRules() {
  try {
    const data = await api("/api/rules");
    const rules = data.rules || [];
    if (document.activeElement !== $("rulesEditor") || !state.rulesLoaded) {
      $("rulesEditor").value = rules.join("\n");
    }
    state.rulesLoaded = true;
    setText("ruleCount", `${rules.filter((r) => r.trim() && !r.trim().startsWith("!") && !r.trim().startsWith("#")).length} 条生效`);
  } catch (e) {
    /* ignore */
  }
}

async function saveRules() {
  const lines = $("rulesEditor").value.split("\n");
  try {
    await api("/api/rules", { method: "POST", body: { rules: lines } });
    toast("规则已保存并生效");
    loadRules();
    refreshStatus();
  } catch (e) {
    toast(e.message, "err");
  }
}

/* ---------- 日志 ---------- */

async function loadLogs(limit = 200) {
  try {
    const data = await api(`/api/logs?limit=${limit}`);
    state.logs = data.logs || [];
    renderLogs();
  } catch (e) {
    /* 核心未启动 */
  }
}

function renderLogs() {
  const body = $("logBody");
  if (!body) return;
  const q = ($("logSearch").value || "").trim().toLowerCase();
  const mode = $("logFilter").value;
  const rows = state.logs.filter((l) => {
    if (mode !== "all" && (l.action || "allowed") !== mode) return false;
    if (!q) return true;
    return (l.domain || "").toLowerCase().includes(q) || (l.rule || "").toLowerCase().includes(q);
  });
  $("logEmpty").hidden = rows.length > 0;
  body.innerHTML = rows
    .map((l) => {
      const action = l.action || "allowed";
      const cls = action === "blocked" ? "blocked" : action === "cached" ? "cached" : "allowed";
      const label = action === "blocked" ? "拦截" : action === "cached" ? "缓存" : "放行";
      return `<tr>
        <td class="time">${esc(l.time || "")}</td>
        <td class="mono">${esc(l.domain || "")}</td>
        <td class="num">${esc(l.qtype || "")}</td>
        <td><span class="tag ${cls}">${label}</span></td>
        <td class="rule" title="${esc(l.rule || "")}">${esc(l.rule || "-")}</td>
        <td class="num">${num(l.elapsed_ms)} ms</td>
        <td class="num">${esc(l.client || "")}</td>
      </tr>`;
    })
    .join("");
}

async function clearLogs() {
  try {
    await api("/api/logs", { method: "DELETE" });
    state.logs = [];
    renderLogs();
    toast("日志已清空");
  } catch (e) {
    toast(e.message, "err");
  }
}

/* ---------- 设置 ---------- */

function renderInfo(info, st) {
  if (!info) return;
  state.info = info;
  const chip = $("chipAdmin");
  chip.innerHTML = `<i class="dot ${info.admin ? "on" : "off"}"></i><span>${info.admin ? "管理员权限" : "普通权限"}</span>`;
  $("adminBadge").className = `badge ${info.admin ? "ok" : "warn"}`;
  $("adminBadge").textContent = info.admin ? "管理员权限" : "需要管理员权限";

  const tk = $("takeoverBadge");
  tk.className = `badge ${info.takenOver ? "ok" : ""}`;
  tk.textContent = info.takenOver ? "已接管系统 DNS" : "未接管";

  const core = $("kvCore");
  core.innerHTML = info.coreRunning
    ? '<span style="color:#a9efc9">运行中</span>'
    : `<span style="color:#ffb3b3">未运行${info.coreError ? "：" + esc(info.coreError) : ""}</span>`;
  setText("kvCorePath", info.corePath || "—");
  setText("kvAdmin", info.admin ? "管理员" : info.adminCapable ? "管理员组成员（未提权，可点右下角提权）" : "标准用户");
  setText("kvData", info.dataDir || "—");
  setText("kvConfig", info.configPath || "—");
  setText("kvVersion", `v${info.version || "1.0.0"}`);
  setText("chipPort", `${st ? st.listen_address : "127.0.0.1"}:${st ? st.listen_port : info.listenPort}`);

  const list = $("adapterList");
  const adapters = info.adapters || [];
  list.innerHTML = adapters.length
    ? adapters
        .map(
          (a) => `<div class="adapter">
            <span class="a-alias">${esc(a.alias)}</span>
            <span class="a-dns">${esc((a.addresses || []).join(", "))}</span>
          </div>`
        )
        .join("")
    : '<div class="empty small">没有检测到已配置 DNS 的 IPv4 网卡</div>';

  // 用户在设置页动过但还没保存时，一律不回写，避免把「53 端口 + 自动接管」打回 25353
  if (!state.settingsDirty) {
    if (st) {
      $("setPort").value = st.listen_port;
      $("setUpstreams").value = (st.upstreams || []).join("\n");
      $("setBlockMode").value = st.block_mode || "nxdomain";
    }
    const mode = info.dnsMode === "system" ? "system" : "custom";
    document.querySelectorAll('input[name="dnsmode"]').forEach((r) => {
      r.checked = r.value === mode;
      r.closest(".mode").classList.toggle("active", r.checked);
    });
    const um = info.upstreamMode === "system" ? "system" : "custom";
    document.querySelectorAll('input[name="upmode"]').forEach((r) => {
      r.checked = r.value === um;
      r.closest(".mode").classList.toggle("active", r.checked);
    });
  }

  const upMode = state.settingsDirty
    ? (document.querySelector('input[name="upmode"]:checked') || { value: "custom" }).value
    : info.upstreamMode === "system"
      ? "system"
      : "custom";

  const guardOn = !!info.guardTask;
  const guardBadge = $("guardBadge");
  guardBadge.textContent = guardOn ? "已注册" : "未注册";
  guardBadge.className = `badge ${guardOn ? "ok" : "warn"}`;
  setText(
    "kvGuard",
    guardOn
      ? "计划任务 ShieldDNS-DNS-Guard 已存在（开机 20s / 登录 10s / 网络连通事件 / 每 15 分钟复查）"
      : "未注册：异常退出或重启后，网卡 DNS 会一直指向 127.0.0.1，需要手动还原"
  );
  $("chkAutoStart").checked = !!info.autoStartEnabled;

  const helperOn = !!info.helperInstalled;
  const helperUp = !!info.helperRunning;
  const helperBadge = $("helperBadge");
  helperBadge.textContent = helperUp ? "运行中" : helperOn ? "已安装·未运行" : "未安装";
  helperBadge.className = `badge ${helperUp ? "ok" : helperOn ? "warn" : ""}`;
  setText(
    "kvHelper",
    helperUp
      ? "任务已安装、助手在线：普通权限也能直接接管/还原系统 DNS，不再弹 UAC"
      : helperOn
        ? "任务已安装但管道连不上：点「重启助手」，或用管理员身份重启面板后再点一次"
        : "未安装：切 53 端口后接管系统 DNS 仍需每次提权"
  );
  const customList =
    info.customUpstreams && info.customUpstreams.length ? info.customUpstreams : info.upstreams || [];
  if (document.activeElement !== $("setUpstreams")) $("setUpstreams").value = customList.join("\n");
  $("upCustom").classList.toggle("hidden", upMode !== "custom");
  $("upSystem").classList.toggle("hidden", upMode !== "system");
  if (upMode === "system") {
    const eff = info.upstreams || [];
    $("upEffective").innerHTML = eff.length
      ? eff.map((u) => `<span class="chip soft">${esc(u)}</span>`).join("")
      : '<span class="chip bad"><i class="dot"></i><span>没有可用的上游</span></span>';
    setText(
      "upSource",
      info.upstreamSource
        ? `来源：${info.upstreamSource}${info.upstreamCapturedAt ? " · " + info.upstreamCapturedAt : ""}`
        : "尚未读取本机 DNS，保存设置时会自动读取"
    );
  }
}

async function loadInfo() {
  try {
    const info = await api("/system/info");
    state.info = info;
    renderInfo(info, state.status);
  } catch (e) {
    /* ignore */
  }
}

async function saveSettings() {
  const mode = document.querySelector('input[name="dnsmode"]:checked').value;
  const upMode = document.querySelector('input[name="upmode"]:checked').value;
  let port = parseInt($("setPort").value, 10);
  if (!port || port < 1 || port > 65535) {
    toast("端口应为 1-65535", "warn");
    return;
  }
  if (mode === "system" && port !== 53) {
    port = 53;
    $("setPort").value = 53;
    toast("自动接管模式已把端口修正为 53", "warn");
  }
  const upstreams = $("setUpstreams")
    .value.split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
  try {
    await api("/system/settings", {
      method: "POST",
      body: {
        listen_port: port,
        dns_mode: mode,
        block_mode: $("setBlockMode").value,
        upstream_mode: upMode,
        upstreams,
        cache_enabled: true,
        log_queries: true,
      },
    });
    toast(upMode === "system" ? "设置已保存，上游已跟随本机 DNS" : "设置已保存，核心已重启");
    state.settingsDirty = false;
    await refreshStatus();
    await loadInfo();
  } catch (e) {
    toast(e.message, "err");
  }
}

async function takeoverDns() {
  try {
    await api("/system/takeover", { method: "POST", body: {} });
    toast("已接管系统 DNS，全部网卡指向 127.0.0.1");
    loadInfo();
  } catch (e) {
    toast(e.message + "（可在下方“以管理员身份重启”后重试）", "err");
  }
}

async function releaseDns() {
  try {
    await api("/system/release", { method: "POST", body: {} });
    toast("已还原系统 DNS");
    loadInfo();
  } catch (e) {
    toast(e.message, "err");
  }
}

async function restartCore() {
  try {
    await api("/system/restart", { method: "POST", body: {} });
    toast("核心已重启");
    refreshStatus();
    loadInfo();
  } catch (e) {
    toast(e.message, "err");
  }
}

async function elevate() {
  try {
    const res = await api("/system/elevate", { method: "POST", body: {} });
    toast(res.message || "已请求管理员权限");
  } catch (e) {
    toast(e.message, "err");
  }
}

/* ---------- 状态刷新 ---------- */

async function refreshStatus() {
  try {
    const st = await api("/api/status");
    state.status = st;
    renderStatus(st);
    if (state.page === "settings") renderInfo(state.info, st);
    return st;
  } catch (e) {
    renderCoreDown();
    return null;
  }
}

async function refreshRecent() {
  try {
    const data = await api("/api/logs?limit=8");
    renderRecent(data.logs || []);
  } catch (e) {
    /* ignore */
  }
}

async function refreshTop() {
  try {
    const data = await api("/api/stats/top");
    renderTop(data.top || []);
  } catch (e) {
    /* ignore */
  }
}

/* ---------- 事件绑定 ---------- */

function bind() {
  document.querySelectorAll(".nav-item").forEach((btn) => {
    btn.addEventListener("click", () => switchPage(btn.dataset.page));
  });
  document.querySelectorAll("[data-goto]").forEach((btn) => {
    btn.addEventListener("click", () => switchPage(btn.dataset.goto));
  });

  $("protectToggle").addEventListener("change", async (ev) => {
    if (state.toggling) return;
    const enabled = ev.target.checked;
    state.toggling = true;
    try {
      await api("/system/protection", { method: "POST", body: { enabled } });
      toast(enabled ? "保护已开启" : "保护已暂停");
      await refreshStatus();
      await loadInfo();
    } catch (e) {
      ev.target.checked = !enabled;
      toast(e.message, "err");
    } finally {
      state.toggling = false;
    }
  });

  $("btnUpdateAll").addEventListener("click", updateAll);
  $("btnUpdateAll2").addEventListener("click", updateAll);
  $("btnAddFilter").addEventListener("click", addFilter);
  $("newFilterUrl").addEventListener("keydown", (e) => {
    if (e.key === "Enter") addFilter();
  });

  $("filterList").addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-act]");
    if (!btn) return;
    const item = btn.closest(".filter-item");
    filterAction(item.dataset.id, btn.dataset.act, true);
  });
  $("filterList").addEventListener("change", (e) => {
    const input = e.target.closest('input[data-act="enable"]');
    if (!input) return;
    const item = input.closest(".filter-item");
    filterAction(item.dataset.id, "enable", input.checked);
  });

  $("btnSaveRules").addEventListener("click", saveRules);
  $("btnClearLogs").addEventListener("click", clearLogs);
  $("logSearch").addEventListener("input", renderLogs);
  $("logFilter").addEventListener("change", renderLogs);
  $("logAuto").addEventListener("change", () => {
    if ($("logAuto").checked) loadLogs();
  });

  $("btnSaveSettings").addEventListener("click", saveSettings);
  $("btnTakeoverDns").addEventListener("click", takeoverDns);
  $("btnReleaseDns").addEventListener("click", releaseDns);
  $("btnRestartCore").addEventListener("click", restartCore);
  $("btnElevate").addEventListener("click", elevate);
  $("quitApp").addEventListener("click", quitApp);
  $("btnQuit2").addEventListener("click", quitApp);

  document.querySelectorAll('input[name="dnsmode"]').forEach((r) => {
    r.addEventListener("change", () => {
      state.settingsDirty = true;
      document.querySelectorAll(".mode[data-mode]").forEach((m) => m.classList.remove("active"));
      r.closest(".mode").classList.add("active");
      if (r.value === "system") {
        $("setPort").value = 53;
      }
    });
  });

  ["setPort", "setUpstreams", "setBlockMode"].forEach((id) => {
    $(id).addEventListener("input", () => {
      state.settingsDirty = true;
    });
  });

  document.querySelectorAll('input[name="upmode"]').forEach((r) => {
    r.addEventListener("change", () => {
      state.settingsDirty = true;
      document.querySelectorAll(".mode[data-upmode]").forEach((m) => m.classList.remove("active"));
      r.closest(".mode").classList.add("active");
      $("upCustom").classList.toggle("hidden", r.value !== "custom");
      $("upSystem").classList.toggle("hidden", r.value !== "system");
    });
  });
  $("btnCaptureUpstreams").addEventListener("click", captureUpstreams);
  $("btnGuardOn").addEventListener("click", () => guardAction("register"));
  $("btnGuardOff").addEventListener("click", () => guardAction("unregister"));
  $("btnGuardTest").addEventListener("click", () => guardAction("run"));
  $("btnHelperOn").addEventListener("click", () => helperAction("register"));
  $("btnHelperOff").addEventListener("click", () => helperAction("unregister"));
  $("btnHelperStart").addEventListener("click", () => helperAction("start"));
  $("chkAutoStart").addEventListener("change", async (e) => {
    const want = e.target.checked;
    try {
      await api("/system/autostart", { method: "POST", body: { enabled: want } });
      toast(want ? "已设置开机自启" : "已取消开机自启");
      await loadInfo();
    } catch (err) {
      toast(err.message, "err");
      e.target.checked = !want;
    }
  });
}

async function helperAction(action) {
  try {
    await api("/system/helper", { method: "POST", body: { action } });
    toast(
      action === "register"
        ? "后台助手已安装并启动，之后接管/还原系统 DNS 不再弹 UAC"
        : action === "unregister"
          ? "后台助手已卸载，接管系统 DNS 将恢复为每次提权"
          : "已请求启动后台助手"
    );
    await loadInfo();
  } catch (e) {
    toast(e.message, "err");
  }
}

async function guardAction(action) {
  try {
    await api("/system/guard", { method: "POST", body: { action } });
    toast(action === "register" ? "看门狗任务已注册" : action === "unregister" ? "看门狗任务已注销" : "已触发一次检查，结果见 watchdog.log");
    await loadInfo();
  } catch (e) {
    toast(e.message, "err");
  }
}

async function captureUpstreams() {
  try {
    const res = await api("/system/upstreams", { method: "POST", body: { action: "capture" } });
    toast(res.restarted ? "已读取本机 DNS 并重启核心" : "已读取本机 DNS（上游无变化）");
    await loadInfo();
    await refreshStatus();
  } catch (e) {
    toast(e.message, "err");
  }
}

function quitApp() {
  if (!confirm("退出 ShieldDNS？退出后将停止 DNS 防护。")) return;
  api("/system/quit", { method: "POST", body: {} }).catch(() => {});
  toast("正在退出…");
}

function switchPage(page) {
  if (!page || !PAGE_META[page]) return;
  state.page = page;
  document.querySelectorAll(".nav-item").forEach((b) => b.classList.toggle("active", b.dataset.page === page));
  document.querySelectorAll(".page").forEach((p) => p.classList.toggle("active", p.id === `page-${page}`));
  const [title, sub] = PAGE_META[page];
  setText("pageTitle", title);
  setText("pageSub", sub);
  if (page === "filters") loadFilters();
  if (page === "rules") loadRules();
  if (page === "logs") loadLogs();
  if (page === "settings") loadInfo();
}

/* ---------- 轮询 ---------- */

async function tick() {
  state.tick += 1;
  await refreshStatus();

  if (state.page === "overview") {
    if (state.tick % 4 === 1) {
      refreshRecent();
      refreshTop();
    }
  }
  if (state.page === "filters" && state.tick % 2 === 0) loadFilters();
  if (state.page === "logs" && $("logAuto").checked && state.tick % 2 === 0) loadLogs();
  if ((state.page === "settings" || state.tick % 6 === 0) && state.tick % 3 === 0) loadInfo();
}

async function boot() {
  bind();
  await refreshStatus();
  await Promise.all([refreshRecent(), refreshTop(), loadInfo()]);
  await loadFilters();
  setInterval(tick, 2000);
}

document.addEventListener("DOMContentLoaded", boot);
