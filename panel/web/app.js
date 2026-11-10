const state = {
  page: "overview",
  status: null,
  info: null,
  filters: [],
  logs: [],
  rulesLoaded: false,
  toggling: false,
  tick: 0,
  // 设置页有未保存的修改时，轮询不能拿服务器旧值回写，否则用户刚点的「53 端口 + 接管」会被旧值覆盖
  settingsDirty: false,
  // 端口被占提示条：用户点过「稍后处理」就不再每次轮询重新弹
  portDismissed: false,
};

const PAGE_META = {
  overview: ["概览", "实时查看防护状态与拦截效果"],
  filters: ["过滤订阅", "通过 URL 订阅 AdGuard 规则并自动更新"],
  rules: ["自定义规则", "手工维护的规则，保存后立即生效"],
  logs: ["查询日志", "每一次经过 ShieldDNS 的域名查询"],
  settings: ["设置", "端口、上游、启动与运行环境"],
};

const $ = (id) => document.getElementById(id);

// ---------- 界面等比缩放 ----------
// 页面按 960×593（黄金比例）的常规尺寸布局，窗口拉小/放大时整体缩放，而不是让卡片被挤压换行。
// 窗口比例已由面板进程锁定为 960:593，所以宽高用同一个系数即可。
const DESIGN_WIDTH = 960;
const MIN_SCALE = 0.7;
const MAX_SCALE = 2.4;

function applyUiScale() {
  const w = window.innerWidth || document.documentElement.clientWidth || DESIGN_WIDTH;
  const scale = Math.min(MAX_SCALE, Math.max(MIN_SCALE, w / DESIGN_WIDTH));
  document.documentElement.style.zoom = scale.toFixed(3);
}

window.addEventListener("resize", applyUiScale);
applyUiScale();

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
    const err = new Error(data.error || data.message || `请求失败（HTTP ${res.status}）`);
    err.data = data;
    throw err;
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
  $("adminBadge").textContent = info.admin ? "管理员权限" : "普通权限";

  const core = $("kvCore");
  core.innerHTML = info.coreRunning
    ? '<span style="color:#a9efc9">运行中</span>'
    : `<span style="color:#ffb3b3">未运行${info.coreError ? "：" + esc(info.coreError) : ""}</span>`;
  setText("kvCorePath", info.corePath || "—");
  setText("kvAdmin", info.admin ? "管理员" : info.adminCapable ? "管理员组成员（未提权，可点右下角提权）" : "标准用户");
  setText("kvData", info.dataDir || "—");
  setText("kvConfig", info.configPath || "—");
  setText("kvVersion", `v${info.version || "0.1.0"}`);
  setText("chipVersion", `v${info.version || "0.1.0"}`);
  setText("chipPort", `${st ? st.listen_address : "127.0.0.1"}:${st ? st.listen_port : info.listenPort}`);

  // DNS 服务状态：只讲端口，不再有"运行模式"概念
  const svc = $("svcBadge");
  if (svc) {
    const running = !!info.coreRunning;
    const blocked = !!info.portOwner;
    svc.className = `badge ${running ? "ok" : "warn"}`;
    svc.textContent = running
      ? `监听 ${info.listenPort}`
      : blocked
        ? `端口 ${info.listenPort} 被占用`
        : "核心未运行";
  }
  const note = $("portNote");
  if (note) {
    note.textContent =
      info.listenPort === 53
        ? "53 是系统解析器唯一会命中的端口；开启防护时自动把本机网卡 DNS 指向 127.0.0.1，无需手动接管。"
        : `当前端口 ${info.listenPort}：只有显式指定该端口的客户端才会经过 ShieldDNS，浏览器与系统解析仍走 53。`;
  }

  // 上游：未勾选=跟随本机，勾选=自定义
  const custom = state.settingsDirty ? $("chkCustomUpstream").checked : info.upstreamMode === "custom";
  if (!state.settingsDirty) {
    if (st) {
      $("setPort").value = st.listen_port;
      $("setUpstreams").value = (st.upstreams || []).join("\n");
      $("setBlockMode").value = st.block_mode || "nxdomain";
    }
  }
  $("chkCustomUpstream").checked = custom;
  const upBadge = $("upModeBadge");
  if (upBadge) {
    upBadge.textContent = custom ? "自定义" : "跟随本机";
    upBadge.className = `badge ${custom ? "ok" : ""}`;
  }
  $("upCustom").classList.toggle("hidden", !custom);
  $("upSystem").classList.toggle("hidden", custom);

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

  const customList = info.customUpstreams && info.customUpstreams.length ? info.customUpstreams : info.upstreams || [];
  if (document.activeElement !== $("setUpstreams")) $("setUpstreams").value = customList.join("\n");

  const helperOn = !!info.helperInstalled;
  const helperUp = !!info.helperRunning;
  const helperBadge = $("helperBadge");
  helperBadge.textContent = helperUp ? "运行中" : helperOn ? "已安装·未运行" : "未安装";
  helperBadge.className = `badge ${helperUp ? "ok" : helperOn ? "warn" : ""}`;
  setText(
    "kvHelper",
    helperUp
      ? "常驻 SYSTEM 身份，负责接管/还原系统 DNS，并在核心掉线时自动还原，全程免 UAC。"
      : helperOn
        ? "任务已安装，助手进程当前没在跑：登录时与每 30 分钟会自动拉起；想立刻恢复可点「以管理员身份重启」后再点「重启」，不需要重新安装。"
        : "未安装：开启防护时的 DNS 接管需要一次 UAC 提权；安装后长期免弹。"
  );
  // 装过就只留「重启 / 卸载」，没装才显示「安装」，避免三个按钮并排挤成一团
  $("btnHelperOn").style.display = helperOn ? "none" : "";
  $("btnHelperStart").style.display = helperOn ? "" : "none";
  $("btnHelperOff").style.display = helperOn ? "" : "none";
  $("chkAutoStart").checked = !!info.autoStartEnabled;
}

async function loadInfo() {
  try {
    const info = await api("/system/info");
    state.info = info;
    renderInfo(info, state.status);
    renderPortBanner(info);
  } catch (e) {
    /* ignore */
  }
}

/* ---------- 端口被占提示 ---------- */

function renderPortBanner(info) {
  const banner = $("portBanner");
  if (!banner) return;
  const owner = info && info.portOwner;
  if (!owner || state.portDismissed) {
    banner.classList.add("hidden");
    return;
  }
  const port = owner.port || (info && info.listenPort) || 53;
  const who = owner.name
    ? `${owner.name}（PID ${owner.pid}）`
    : `PID ${owner.pid} 的进程`;
  setText("portBannerTitle", `端口 ${port} 被占用，DNS 防护无法启动`);
  setText(
    "portBannerBody",
    `${who} 正在监听 ${port}。系统解析器只会把查询发到 53，所以 ShieldDNS 不会自动换端口——结束该进程后点右侧按钮重试即可。`
  );
  banner.classList.remove("hidden");
}

async function killPortOwner() {
  const btn = $("btnKillPort");
  const port = (state.info && state.info.listenPort) || 53;
  btn.disabled = true;
  btn.textContent = "正在结束…";
  try {
    await api("/system/port", { method: "POST", body: { action: "kill", port } });
    state.portDismissed = false;
    toast("已结束占用进程，核心已重新启动");
    await refreshStatus();
    await loadInfo();
    if (state.info && !state.info.coreRunning) {
      toast("核心仍未就绪，请在设置页查看核心日志", "err");
    }
  } catch (e) {
    if (e.data && e.data.portOwner) {
      state.portDismissed = false;
      renderPortBanner({ portOwner: e.data.portOwner, listenPort: port });
    }
    toast(e.message, "err");
  } finally {
    btn.disabled = false;
    btn.textContent = "结束占用进程并重试";
  }
}

// 检查更新：只查 GitHub Release，不自动替换正在运行的 exe
async function checkUpdate(force, quiet) {
  const notify = quiet ? () => {} : toast;
  const btn = $("btnCheckUpdate");
  const note = $("updateNote");
  const text = $("updateText");
  const openBtn = $("btnOpenRelease");
  if (btn) {
    btn.disabled = true;
    btn.textContent = "检查中…";
  }
  try {
    const res = await api(`/system/update${force ? "?force=1" : ""}`);
    note.classList.remove("hidden");
    openBtn.classList.add("hidden");
    if (!res.ok) {
      state.update = res;
      text.textContent = `检查失败：${res.error || "未知错误"}（已将版本 v${res.current}）`;
      notify("检查更新失败，请检查网络或代理", "err");
      return;
    }
    state.update = res;
    if (res.hasUpdate) {
      const size = res.downloadSize ? ` · ${(res.downloadSize / 1048576).toFixed(1)} MB` : "";
      text.textContent = `发现新版本 v${res.latest}（当前 v${res.current}）${size}`;
      openBtn.classList.remove("hidden");
      notify(`发现新版本 v${res.latest}`);
    } else {
      text.textContent = `已是最新版本 v${res.current}（GitHub 最新 Release：v${res.latest}）`;
      notify("已是最新版本");
    }
  } catch (e) {
    note.classList.remove("hidden");
    openBtn.classList.add("hidden");
    text.textContent = `检查失败：${e.message}`;
    notify(e.message, "err");
  } finally {
    if (btn) {
      btn.disabled = false;
      btn.textContent = "检查更新";
    }
  }
}

function openReleasePage() {
  const url = (state.update && state.update.releaseURL) || "https://github.com/sayaxia/ShieldDNS/releases/latest";
  api("/system/openrelease", { method: "POST", body: { url } })
    .then(() => toast("已在浏览器打开下载页"))
    .catch((e) => toast(e.message, "err"));
}

async function saveSettings() {  let port = parseInt($("setPort").value, 10);
  if (!port || port < 1 || port > 65535) {
    toast("端口应为 1-65535", "warn");
    return;
  }
  const upMode = $("chkCustomUpstream").checked ? "custom" : "system";
  const upstreams = $("setUpstreams")
    .value.split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
  try {
    await api("/system/settings", {
      method: "POST",
      body: {
        listen_port: port,
        dns_mode: port === 53 ? "system" : "custom",
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
      if (e.data && e.data.portOwner) {
        state.portDismissed = false;
        renderPortBanner({ portOwner: e.data.portOwner });
      }
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
  $("btnKillPort").addEventListener("click", killPortOwner);
  $("btnHidePort").addEventListener("click", () => {
    state.portDismissed = true;
    $("portBanner").classList.add("hidden");
  });
  $("btnRestartCore").addEventListener("click", restartCore);
  if ($("btnCheckUpdate")) $("btnCheckUpdate").addEventListener("click", () => checkUpdate(true));
  if ($("btnOpenRelease")) $("btnOpenRelease").addEventListener("click", openReleasePage);
  $("btnElevate").addEventListener("click", elevate);
  $("quitApp").addEventListener("click", quitApp);
  $("btnQuit2").addEventListener("click", quitApp);

  ["setPort", "setUpstreams", "setBlockMode"].forEach((id) => {
    $(id).addEventListener("input", () => {
      state.settingsDirty = true;
    });
  });

  // 上游只有一个勾选框：勾上=自定义，不勾=跟随本机
  $("chkCustomUpstream").addEventListener("change", (e) => {
    state.settingsDirty = true;
    const custom = e.target.checked;
    $("upCustom").classList.toggle("hidden", !custom);
    $("upSystem").classList.toggle("hidden", custom);
    const badge = $("upModeBadge");
    if (badge) {
      badge.textContent = custom ? "自定义" : "跟随本机";
      badge.className = `badge ${custom ? "ok" : ""}`;
    }
  });
  $("btnCaptureUpstreams").addEventListener("click", captureUpstreams);
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
  checkUpdate(false, true);
  setInterval(tick, 2000);
}

document.addEventListener("DOMContentLoaded", boot);
