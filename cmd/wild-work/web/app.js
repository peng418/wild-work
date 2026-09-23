// wild-work 管理面板前端（原生 JS，无构建步骤，直接 fetch 管理 API）
"use strict";

const $ = (id) => document.getElementById(id);

// ---------- API 封装 ----------
async function api(path, body) {
  const opts = { method: "GET", headers: { "Content-Type": "application/json" } };
  if (body !== undefined) {
    opts.method = "POST";
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch(path, opts);
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    throw new Error(data.error || ("请求失败 " + resp.status));
  }
  return data;
}

// ---------- 工具 ----------
function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

let toastTimer = null;
function toast(msg) {
  const t = $("toast");
  t.textContent = msg;
  t.classList.remove("hidden");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add("hidden"), 3000);
}

function shortUid(uid) {
  if (!uid) return "";
  return uid.length <= 12 ? uid : uid.slice(0, 6) + "…" + uid.slice(-4);
}

// ---------- 全局状态 ----------
let state = null;

// ---------- 数据加载 ----------
async function loadState() {
  state = await api("/api/state");
  // 账号数据已变（刷新/签到/增删），明细缓存随之失效，避免 tooltip 展示旧余额。
  detailCache = {};
  render();
}

async function loadFees() {
  try {
    const fees = await api("/api/fees");
    renderFees(fees);
    // 测速缓存加载后再渲染一次（保证指示灯显示）
    if (Object.keys(benchCache).length === 0) {
      await loadBenchmarkState();
      if (Object.keys(benchCache).length > 0) renderFees(fees);
    }
  } catch (e) { /* 费率接口失败不阻塞 */ }
}

async function refreshFees() {
  $("btnRefreshFees").disabled = true;
  try {
    await api("/api/fees/refresh", {});
    const fees = await api("/api/fees");
    renderFees(fees);
    toast("模型列表和费率已刷新");
  } catch (e) { toast(e.message); } finally {
    $("btnRefreshFees").disabled = false;
  }
}

// ---------- 模型测速：SSE 流式并行 + 指示灯 ----------
let benchCache = {};       // { modelID: {latency_ms, status, error} }
let benchTestedAt = null;  // 上次测速时间字符串
let benchRunning = false;

// benchBase 返回直连 wild-work 的 base URL（绕过 fnOS 网关以支持 SSE 流式传输）
function benchBase() {
  // 优先取当前页面的 port：直连时 location.port 就是 5013，走网关时回退到 5013
  const port = location.port || "5013";
  // 网关代理（80/443）下 port 是 80/443，需要强制走 5013
  if (port === "80" || port === "443") {
    return location.protocol + "//" + location.hostname + ":5013";
  }
  return location.protocol + "//" + location.hostname + ":" + port;
}

// loadBenchmarkState 从后端拉取上次持久化的测速结果
async function loadBenchmarkState() {
  try {
    const resp = await fetch(benchBase() + "/api/benchmark/state");
    const data = await resp.json();
    if (data && data.results) {
      benchCache = data.results;
      benchTestedAt = data.tested_at || null;
      updateBenchmarkTime();
    }
  } catch (e) { /* 无缓存不报错 */ }
}

function updateBenchmarkTime() {
  const el = $("benchTime");
  if (benchTestedAt) {
    const d = new Date(benchTestedAt);
    const str = d.getFullYear() + "-" +
      String(d.getMonth() + 1).padStart(2, "0") + "-" +
      String(d.getDate()).padStart(2, "0") + " " +
      String(d.getHours()).padStart(2, "0") + ":" +
      String(d.getMinutes()).padStart(2, "0");
    el.textContent = "上次测速：" + str;
    el.style.display = "";
  } else {
    el.textContent = "";
    el.style.display = "none";
  }
}

// speedDot 返回模型速度指示灯 HTML
// 白灯=未测, 绿灯=<300ms, 黄灯=≥300ms, 红灯=报错(带tooltip)
function speedDot(modelFull) {
  const r = benchCache[modelFull];
  if (!r) {
    return `<span class="speed-dot speed-white" title="未测速">○</span>`;
  }
  if (r.status === "error") {
    const errShort = (r.error || "未知错误").substring(0, 80);
    return `<span class="speed-dot speed-red" title="${esc(errShort)}">✗</span>`;
  }
  const ms = Math.round(r.latency_ms);
  const cls = ms < 300 ? "speed-green" : "speed-yellow";
  return `<span class="speed-dot ${cls}" title="首token ${ms}ms">${ms}ms</span>`;
}

// speedCell 返回速度列 HTML（灯 + 数字）
function speedCell(modelFull) {
  const r = benchCache[modelFull];
  if (!r) {
    return `<span class="speed-cell"><span class="speed-dot speed-white" title="未测速">○</span></span>`;
  }
  if (r.status === "error") {
    const errShort = (r.error || "未知错误").substring(0, 80);
    return `<span class="speed-cell"><span class="speed-dot speed-red has-tip" title="${esc(errShort)}">✗</span></span>`;
  }
  const ms = Math.round(r.latency_ms);
  const cls = ms < 300 ? "speed-green" : "speed-yellow";
  return `<span class="speed-cell"><span class="speed-dot ${cls}">${ms}ms</span></span>`;
}

async function runBenchmark() {
  if (benchRunning) return;
  benchRunning = true;
  const btn = $("btnBenchmark");
  btn.disabled = true;
  btn.textContent = "⚡ 测速中...";

  // 清空旧结果
  benchCache = {};
  benchTestedAt = null;
  updateBenchmarkTime();
  // 重绘费率表以显示白灯
  if (state) {
    try {
      const fees = await api("/api/fees");
      renderFees(fees);
    } catch (e) { /* ignore */ }
  }

  try {
    const resp = await fetch(benchBase() + "/api/benchmark");
    const reader = resp.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    let done = false;
    let tested = 0;

    while (!done) {
      const { value, done: streamDone } = await reader.read();
      if (value) buffer += decoder.decode(value, { stream: true });
      // 按行处理 SSE
      const lines = buffer.split("\n");
      buffer = lines.pop(); // 保留未完整行
      for (const line of lines) {
        if (!line.startsWith("data: ")) continue;
        const json = line.slice(6);
        const data = JSON.parse(json);
        if (data.done) {
          done = true;
          benchTestedAt = new Date().toISOString();
          updateBenchmarkTime();
          tested = data.total || 0;
        } else {
          // 单个模型结果
          benchCache[data.model] = {
            latency_ms: data.latency_ms,
            status: data.status,
            error: data.error || "",
          };
          tested++;
          // 更新该模型所在行的速度单元格
          updateSpeedCells();
          // 更新按钮提示
          btn.textContent = `⚡ ${tested}个...`;
        }
      }
    }
  } catch (e) {
    toast("测速失败：" + e.message);
  } finally {
    benchRunning = false;
    btn.disabled = false;
    btn.textContent = "⚡ 测速";
    // 最后一次全量重绘确保一致性
    try {
      const fees = await api("/api/fees");
      renderFees(fees);
    } catch (e) { /* ignore */ }
  }
}

// updateSpeedCells 仅更新已渲染 DOM 中的速度单元格（增量更新，不重绘全表）
function updateSpeedCells() {
  document.querySelectorAll(".speed-cell").forEach(cell => {
    const model = cell.dataset.model;
    if (!model) return;
    const r = benchCache[model];
    if (!r) return;
    if (r.status === "error") {
      const errShort = (r.error || "未知错误").substring(0, 80);
      cell.innerHTML = `<span class="speed-dot speed-red has-tip" title="${esc(errShort)}">✗</span>`;
    } else {
      const ms = Math.round(r.latency_ms);
      const cls = ms < 300 ? "speed-green" : "speed-yellow";
      cell.innerHTML = `<span class="speed-dot ${cls}">${ms}ms</span>`;
    }
  });
}

// ---------- 积分明细 tooltip ----------
// 明细条数可能很多（TraeWork 每个签到奖励都是独立条目，常见 30+），
// 故分页展示：每页 DETAIL_PAGE_SIZE 条，页码状态存在 detailState 里，
// 翻页不重新请求（detailCache 已缓存该账号的完整响应）。
const DETAIL_PAGE_SIZE = 8;
let detailTimer = null;
let detailCache = {};
// detailState 当前 tooltip 的展示状态：{uid, page}。
// detailState 当前 tooltip 的展示状态：{uid, page}。
let detailState = null;

async function showCreditDetail(e, uid) {
  const el = e.currentTarget;
  if (detailTimer) { clearTimeout(detailTimer); detailTimer = null; }

  // 切换到另一个账号时重置页码；同一账号重复 hover 保留原页码。
  const page = (detailState && detailState.uid === uid) ? detailState.page : 0;

  let d = detailCache[uid];
  if (!d) {
    try {
      d = await api("/api/account/resource_detail", { uid });
      detailCache[uid] = d;
    } catch (err) { return; }
  }
  if (!d || !d.items || !d.items.length) return;

  let tip = $("creditTip");
  if (!tip) {
    tip = document.createElement("div");
    tip.id = "creditTip";
    tip.className = "credit-tip";
    document.body.appendChild(tip);
  }

  const rect = el.getBoundingClientRect();
  detailState = { uid, page };
  renderCreditDetail();
  // 先渲染再量尺寸，才能决定向上还是向下弹出。
  const h = tip.offsetHeight;
  let top = rect.bottom + 4;
  if (top + h > window.innerHeight) top = Math.max(4, rect.top - h - 4);
  let left = rect.left;
  if (left + tip.offsetWidth > window.innerWidth) left = Math.max(4, window.innerWidth - tip.offsetWidth - 10);
  tip.style.left = left + "px";
  tip.style.top = top + "px";
}

// renderCreditDetail 按 detailState 重绘 tooltip（含分页控件与可用/不可用小计）。
function renderCreditDetail() {
  const tip = $("creditTip");
  if (!tip || !detailState) return;
  const d = detailCache[detailState.uid];
  if (!d || !d.items || !d.items.length) return;

  const items = d.items;
  const pages = Math.max(1, Math.ceil(items.length / DETAIL_PAGE_SIZE));
  const page = Math.min(Math.max(0, detailState.page), pages - 1);
  detailState.page = page;
  const slice = items.slice(page * DETAIL_PAGE_SIZE, (page + 1) * DETAIL_PAGE_SIZE);

  // 有效期列仅当上游确实下发了到期时间时才出现——渠道未返回则不显示该列，
  // 避免一列全空或把「无到期信息」误读成「永不过期」。
  const hasExpiry = items.some((it) => it.expire_at);

  // 头部：翻页器在顶部居中，标题与条数分列两端（用户方案：翻页不跨出浮窗）。
  let html = `<div class="detail-head">`;
  html += `<span class="detail-count">共 ${items.length} 条</span>`;
  if (pages > 1) {
    html += `<span class="detail-pager">`;
    html += `<span class="detail-pg${page === 0 ? " off" : ""}" data-pg="${page - 1}">‹</span>`;
    html += `<span class="detail-pg-info">${page + 1} / ${pages}</span>`;
    html += `<span class="detail-pg${page >= pages - 1 ? " off" : ""}" data-pg="${page + 1}">›</span>`;
    html += `</span>`;
  }
  html += `<span class="detail-title">积分明细</span></div>`;
  html += `<table class="detail-table"><thead><tr><th>套餐</th><th>总额</th><th>已用</th><th>剩余</th>`;
  if (hasExpiry) html += `<th>有效期</th>`;
  html += `</tr></thead><tbody>`;
  for (const it of slice) {
    // 不可用额度整行淡显 + 角标，与可用额度区分开（如 TraeWork 的官方客户端专用池）。
    const cls = it.usable ? "" : ' class="detail-unusable"';
    const tag = it.usable ? "" : '<span class="detail-tag" title="该额度仅供官方客户端使用，本工具无法消耗">不可用</span>';
    html += `<tr${cls}><td>${esc(it.name)}${tag}</td><td>${it.total}</td><td>${it.used}</td><td>${it.remain}</td>`;
    if (hasExpiry) html += `<td>${it.expire_at ? esc(it.expire_at) : "-"}</td>`;
    html += `</tr>`;
  }
  html += `</tbody></table>`;

  // 小计行：只在确实存在不可用额度时才拆开展示，否则保持单数字（不制造无意义的 0）。
  const usable = d.usable_remain || 0;
  const unusable = d.unusable_remain || 0;
  html += `<div class="detail-sum">`;
  html += `<span>可用 <b>${usable}</b></span>`;
  if (unusable > 0) html += `<span class="detail-sum-unusable">不可用 <b>${unusable}</b></span>`;
  html += `</div>`;
  tip.innerHTML = html;
  tip.style.display = "block";

  // 翻页：只改状态重绘，不重新请求接口。事件重挂由 hideCreditDetail 统一负责。
  tip.querySelectorAll(".detail-pg").forEach((btn) => {
    if (btn.classList.contains("off")) return;
    btn.onclick = (ev) => {
      ev.stopPropagation();
      if (detailTimer) { clearTimeout(detailTimer); detailTimer = null; } // 翻页即取消关闭
      const target = Number(btn.dataset.pg);
      if (Number.isFinite(target)) { detailState.page = target; renderCreditDetail(); }
    };
  });
}

function hideCreditDetail() {
  detailTimer = setTimeout(() => {
    const tip = $("creditTip");
    if (tip) tip.style.display = "none";
  }, 300);
  const tip = $("creditTip");
  if (tip) {
    tip.onmouseenter = () => { if (detailTimer) { clearTimeout(detailTimer); detailTimer = null; } };
    tip.onmouseleave = () => { tip.style.display = "none"; };
  }
}

// ---------- 渲染 ----------
function render() {
  renderTopbar();
  renderAccounts();
  renderTimes();
  $("chkAutostart").checked = state.autostart;
}

function renderTopbar() {
  $("ver").textContent = "v" + state.version;
  $("serverLine").textContent = state.running ? "服务运行中" : "服务未启动";
  $("aboutVer").textContent = state.version;

  const host = (state.listen_host === "0.0.0.0" || state.listen_host === "" || state.listen_host === "::")
    ? "127.0.0.1" : state.listen_host;
  const apiURL = `http://${host}:${state.listen_port}/v1`;
  const apiURLV2 = `http://${host}:${state.listen_port}/v2`;
  $("apiAddr").querySelector(".val").textContent = apiURL;
  $("apiAddrV2").querySelector(".val").textContent = apiURLV2;

  const key = state.api_key;
  $("apiKeyDisplay").querySelector(".val").textContent = key === "" ? "（无鉴权）" : key;

  const keyV2 = state.api_key_v2;
  $("apiKeyDisplayV2").querySelector(".val").textContent = keyV2 === "" ? "（无鉴权）" : keyV2;
}

// 渠道显示名与 CSS 短类名（后端 group / 费率 channel 均为 provider.Kind）。
const CH_LABEL = { workbuddy: "WorkBuddyCN", workbuddyai: "WorkBuddyAI", traework: "TraeWork", qoder: "Qoder", qodercn: "QoderCN", qodercom: "QoderCOM", qwenwork: "千问办公" };
const CH_CLASS = { workbuddy: "wb", workbuddyai: "wbai", traework: "trae", qoder: "qoder", qodercn: "qodercn", qodercom: "qodercom", qwenwork: "qwenwork" };
const chLabel = (k) => CH_LABEL[k] || "WorkBuddy";
const chClass = (k) => CH_CLASS[k] || "wb";
// 不支持显式签到（手动按钮）的渠道：
// WorkBuddy 国际版不提供手动签到，而是自动对话保活领日活奖励；千问办公无签到活动。
const NO_EXPLICIT_CHECKIN = new Set(["workbuddyai", "qwenwork"]);
const noExplicitCheckin = (g) => NO_EXPLICIT_CHECKIN.has(g);
// 无手动签到渠道的状态文案：国际版是「自动领日活奖励」，千问办公为「无签到」。
const NO_CHECKIN_TAG = { workbuddyai: "自动领日活奖励" };
const noCheckinText = (g) => NO_CHECKIN_TAG[g] || "无签到";

// creditsText 账号卡片的积分文案。
// 拆成「可用 / 不可用 / 临期」三个数字：渠道（如 TraeWork）会下发官方客户端专用的
// 额度池，对本工具是看得见用不了的，混进一个数字会让人误判可用余额；
// 临期是可消耗余额中 24h 内（到期日≤明天）到期的部分，提示优先消耗。
// 不可用仅在 >0 时显示；临期同理，凭空多个灰/红 0 很吵。
// 旧版本 state 文件（v2.2.0 及之前，无 unusable 字段）读入后 credits_stale=true，
// 此时不把旧值当真值，改显示「待刷新」；自动刷新首刷成功后即变回真实拆分。
function creditsText(a) {
  if (a.credits_stale) {
    return `<span class="credit-stale" title="余额口径已过期（旧版本状态文件），正在自动刷新…">待刷新</span>`;
  }
  let html = `<span class="credit-num">${a.credits}</span><span>可用积分</span>`;
  if ((a.expiring_credits || 0) > 0) {
    html += `<span class="credit-expiring"> (临期${a.expiring_credits})</span>`;
  }
  if ((a.unusable_credits || 0) > 0) {
    html += `<span class="credit-unusable"> (不可用${a.unusable_credits})</span>`;
  }
  return html;
}

function renderAccounts() {
  const grid = $("acctList");
  const empty = $("acctEmpty");
  if (!state.accounts.length) {
    grid.innerHTML = "";
    empty.classList.remove("hidden");
    return;
  }
  empty.classList.add("hidden");

  grid.innerHTML = state.accounts.map((a) => {
    const group = chClass(a.group);
    const groupName = chLabel(a.group);
    const noCheckin = noExplicitCheckin(a.group); // 无显式签到，签到按钮灰掉
    const noCheckinTitle = a.group === "workbuddyai"
      ? "无需手动签到：定时自动对话保活并领取日活奖励"
      : `${groupName} 不支持手动签到`;

    const checkinTag = a.last_checkin_at
      ? `<span class="tag ${a.last_checkin_ok ? "ok" : "bad"}">${a.last_checkin_ok ? "签到成功" : "签到失败"}</span>`
      : (noCheckin ? `<span class="tag neutral" title="${esc(noCheckinTitle)}">${noCheckinText(a.group)}</span>` : '<span class="tag neutral">未签到</span>');

    const disabledClass = a.disabled ? " disabled" : "";
    const disableIcon = a.disabled ? "▶" : "⏸";
    const disableTitle = a.disabled ? "启用" : "停用";
    const checkinBtn = noCheckin
      ? `<span class="icon-op off" title="${esc(noCheckinTitle)}" onclick="return false">✓</span>`
      : `<span class="icon-op" title="签到" onclick="checkin('${a.uid}')">✓</span>`;

    return `
    <div class="acct-card${disabledClass}">
      <div class="acct-top">
        <div>
          <span class="badge ${group}">${groupName}</span>
          <span class="acct-name">${esc(a.nickname || shortUid(a.uid))}</span>
        </div>
        <div class="acct-ops">
          ${checkinBtn}
          <span class="icon-op" title="刷新积分" onclick="refreshOne('${a.uid}')">↻</span>
          <span class="icon-op warn" title="${disableTitle}" onclick="toggleDisable('${a.uid}',${a.disabled})">${disableIcon}</span>
          <span class="icon-op danger" title="删除账号" onclick="removeAcct('${a.uid}')">✕</span>
        </div>
      </div>
      <div class="acct-uid">UID: ${esc(shortUid(a.uid))}</div>
      <div class="acct-mid">
        <div class="acct-credits" onmouseenter="showCreditDetail(event,'${a.uid}')" onmouseleave="hideCreditDetail()">${creditsText(a)}</div>
        <div class="acct-checkin">${checkinTag}</div>
      </div>
    </div>`;
  }).join("");
}

function renderFees(fees) {
  const box = $("feesBox");
  const channels = fees.channels || [];

  if (channels.length === 0) {
    box.innerHTML = `<div class="note">${esc(fees.note || "")}</div>
      <div class="note">${esc(fees.disclaimer || "")}</div>`;
    return;
  }

  let html = `<div class="note">${esc(fees.note || "")}</div>`;
    if (fees.cached_at) html += `<div class="note">费率上次更新：${esc(fees.cached_at)}</div>`;
    if (fees.error) html += `<div class="note" style="color:var(--danger)">${esc(fees.error)}</div>`;

    html += `<table><thead><tr><th>模型</th><th>速度</th><th>倍率</th><th>模型</th><th>速度</th><th>倍率</th></tr></thead><tbody>`;

  const UNKNOWN_TIP = "上游未返回，请在客户端自行确认";

  // 能力图标：模型 ID 后的小标记，title 属性提供文字描述。
  // 只展示上游明确声明的能力；未声明的（字段缺失或上游返回 false）不显示图标。
  const capIcons = (m) => {
    if (!m) return "";
    const caps = [];
    if (m.supports_images) {
      caps.push(`<span class="cap-icon cap-img" title="支持图像输入（多模态视觉）：可直接发送图片给该模型">👁</span>`);
    }
    if (m.supports_reasoning) {
      caps.push(`<span class="cap-icon cap-reason" title="支持思考/推理模式：回复前会进行推理（可能含 reasoning_content）">🧠</span>`);
    }
    if (m.supports_tools) {
      caps.push(`<span class="cap-icon cap-tool" title="支持函数/工具调用（tool_calls）">🔧</span>`);
    }
    return caps.length > 0 ? ` <span class="cap-icons">${caps.join("")}</span>` : "";
  };

  // 能力文字摘要，拼进模型 tooltip。
  // 措辞说明：上游模型列表接口未声明某能力时，本工具不自行断言其「不支持」，
  // 只说「未声明」——避免把缺失信息当成否定结论。
  const capText = (m) => {
    if (!m) return null;
    const yes = [], unknown = [];
    (m.supports_images ? yes : unknown).push("图像输入");
    (m.supports_reasoning ? yes : unknown).push("思考模式");
    (m.supports_tools ? yes : unknown).push("工具调用");
    const parts = [];
    if (yes.length) parts.push(`支持：${yes.join("、")}`);
    if (unknown.length) parts.push(`上游未声明：${unknown.join("、")}`);
    return parts.join("；");
  };

  // 模型 id 的 tooltip：能拿到上下文则展示，否则明确说未知
  const modelTip = (m) => {
    const parts = [`模型：${m.model}`];
    if (m.has_context && m.context_window) {
      parts.push(`上下文窗口：${fmtTokens(m.context_window)}`);
      if (m.max_tokens) parts.push(`最大输出：${fmtTokens(m.max_tokens)}`);
    } else {
      parts.push("上下文窗口：未知");
      parts.push("最大输出：未知");
      parts.push("（上游未提供该信息）");
    }
    const ct = capText(m);
    if (ct) parts.push(ct);
    return parts.join("\n");
  };

  // 单行倍率单元格
  const rateCell = (m) => {
    if (!m) return "";
    if (!m.priced) {
      return `<span class="rate-unknown" title="${esc(UNKNOWN_TIP)}">unknown</span>`;
    }
    if (m.free) {
      return `<span class="rate-free" title="上游标注为免费（x0.00）">✦ Free</span>`;
    }
    return `<span class="rate-paid">x${m.rate.toFixed(2)}</span>`;
  };

  // 促销标签：使用上游给的颜色（原本被拼在文案里没解析）
  const noteCell = (m) => {
    if (!m || !m.note) return "";
    const style = m.color ? ` style="color:${esc(m.color)}"` : "";
    return ` <span class="rate-note"${style}>${esc(m.note)}</span>`;
  };

  for (const ch of channels) {
    const chName = chLabel(ch.channel);
    const chCls = chClass(ch.channel);
    const models = ch.models || [];
    html += `<tr class="ch-header ${chCls}"><td colspan="6">${esc(chName)}</td></tr>`;
        // 每行两个模型
        for (let i = 0; i < models.length; i += 2) {
          const m1 = models[i];
          const m2 = models[i + 1];
          const id1 = m1 ? `<code title="${esc(modelTip(m1))}">${esc(m1.model)}</code>${capIcons(m1)}${noteCell(m1)}` : "";
          const id2 = m2 ? `<code title="${esc(modelTip(m2))}">${esc(m2.model)}</code>${capIcons(m2)}${noteCell(m2)}` : "";
          const sp1 = m1 ? `<span class="speed-cell" data-model="${esc(ch.channel + '/' + m1.model)}">${speedCell(ch.channel + '/' + m1.model)}</span>` : "";
          const sp2 = m2 ? `<span class="speed-cell" data-model="${esc(ch.channel + '/' + m2.model)}">${speedCell(ch.channel + '/' + m2.model)}</span>` : "";
          html += `<tr><td>${id1}</td><td>${sp1}</td><td>${rateCell(m1)}</td><td>${id2}</td><td>${sp2}</td><td>${rateCell(m2)}</td></tr>`;
    }
  }

  html += `</tbody></table>`;
  html += `<div class="note" style="margin-top:8px">${esc(fees.disclaimer || "")}</div>`;
  box.innerHTML = html;
}

// fmtTokens 把 token 数格式化为 1M / 192k 形式。
function fmtTokens(n) {
  if (!n) return "-";
  if (n >= 1000000 && n % 1000000 === 0) return `${n / 1000000}M`;
  if (n >= 1000) return `${Math.round(n / 1000)}k`;
  return String(n);
}

// ---------- 账号操作 ----------
async function checkin(uid) {
  try {
    const r = await api("/api/account/checkin", { uid });
    toast(r.ok ? `签到成功：${r.msg}（剩余 ${r.remain}）` : `签到：${r.msg}`);
    loadState();
  } catch (e) { toast(e.message); }
}

async function refreshOne(uid) {
  try {
    const r = await api("/api/account/refresh", { uid });
    toast(`刷新成功：剩余积分 ${r.remain}`);
    loadState();
  } catch (e) { toast(e.message); }
}

async function toggleDisable(uid, currentlyDisabled) {
  const action = currentlyDisabled ? "启用" : "停用";
  confirmDialog(`确定${action}该账号？${currentlyDisabled ? "" : "停用后路由不会分配给该账号。"}`, async () => {
    try {
      await api("/api/account/disable", { uid, disabled: !currentlyDisabled });
      toast(`账号已${action}`);
      loadState();
    } catch (e) { toast(e.message); }
  });
}

function removeAcct(uid) {
  confirmDialog("确定删除该账号？删除后需重新登录。", async () => {
    try {
      await api("/api/account/remove", { uid });
      toast("账号已删除");
      loadState();
    } catch (e) { toast(e.message); }
  });
}

// ---------- 批量操作 ----------
async function checkinAll() {
  $("btnCheckinAll").disabled = true;
  try {
    const r = await api("/api/account/checkin_all", {});
    const ok = (r.results || []).filter((x) => x.ok).length;
    const skip = (state.accounts || []).filter((a) => noExplicitCheckin(a.group)).length;
    const total = (r.results || []).length + skip;
    toast(skip ? `批量签到完成：成功 ${ok} / ${total}（${skip} 个账号无签到活动跳过）` : `批量签到完成：成功 ${ok} / 共 ${total}`);
    loadState();
  } catch (e) { toast(e.message); } finally {
    $("btnCheckinAll").disabled = false;
  }
}

async function refreshAll() {
  $("btnRefreshAll").disabled = true;
  try {
    const r = await api("/api/account/refresh_all", {});
    toast(r.busy ? "已有刷新任务进行中" : `积分刷新完成：成功 ${r.ok} / 失败 ${r.failed}`);
    loadState();
  } catch (e) { toast(e.message); } finally {
    $("btnRefreshAll").disabled = false;
  }
}

// ---------- 登录 ----------
let pendingChannel = null;
// 无手动签到渠道的登录提示差异文案（国际版会自动领日活奖励）。
const NO_CHECKIN_LOGIN_HINT = {
  workbuddyai: "（无需手动签到，定时自动对话保活并领取日活奖励）",
  qwenwork: "（每日积分服务端 00:00 自动发放；若浏览器已登录千问办公则全自动完成，否则需扫码一次）",
};
function promptLogin(channel) {
  pendingChannel = channel;
  const name = chLabel(channel);
  $("lcTitle").textContent = "添加 " + name + " 账号";
  $("lcMsg").textContent = noExplicitCheckin(channel)
    ? `点击「登录${name}」将打开浏览器窗口，请按照指示正常登录${name}账号，登录成功后关闭浏览器窗口即可。${NO_CHECKIN_LOGIN_HINT[channel] || ""}`
    : `点击「登录${name}」将打开浏览器窗口，请按照指示正常登录${name}账号，登录成功后关闭浏览器窗口即可。`;
  $("btnLoginConfirm").textContent = "登录" + name;
  $("loginConfirmOverlay").classList.remove("hidden");
}
function confirmLogin() {
  $("loginConfirmOverlay").classList.add("hidden");
  if (pendingChannel) startLogin(pendingChannel);
}

async function startLogin(channel) {
  try {
    const r = await api("/api/login/start", { channel });
    const url = r.auth_url;
    if (!url) { toast("无法获取登录链接"); return; }
    $("loginTitle").textContent = `添加 ${chLabel(channel)} 账号`;
    $("loginMsg").textContent = "请在浏览器新窗口中完成登录…";
    $("loginOverlay").classList.remove("hidden");
    $("btnCopyUrl").dataset.url = url;
    window.open(url, "_blank", "noopener,noreferrer");
    startLoginPoll();
  } catch (e) {
    toast(e.message);
  }
}

async function cancelLogin() {
  try {
    await api("/api/login/cancel", {});
    stopLoginPoll();
    $("loginOverlay").classList.add("hidden");
    toast("登录已取消");
  } catch (e) { toast(e.message); }
}

function copyUrl() {
  const url = $("btnCopyUrl").dataset.url;
  if (!url) { toast("暂无链接"); return; }
  navigator.clipboard.writeText(url).then(() => toast("链接已复制")).catch(() => toast("复制失败，请手动复制"));
}

// 登录轮询
let loginPoll = null;
function startLoginPoll() {
  stopLoginPoll();
  loginPoll = setInterval(async () => {
    try {
      const st = await api("/api/state");
      if (!st.login_busy) {
        stopLoginPoll();
        $("loginOverlay").classList.add("hidden");
        toast("登录完成，正在同步账号…");
        await loadState();
        refreshFees();
      }
    } catch (e) { /* 忽略 */ }
  }, 3000);
}
function stopLoginPoll() {
  if (loginPoll) { clearInterval(loginPoll); loginPoll = null; }
}

// ---------- 签到时间 ----------
async function saveCheckinTime() {
  const sel = $("checkinTime");
  if (!sel) return;
  const h = parseInt(sel.value, 10);
  if (isNaN(h) || h < 0 || h > 23) { toast("无效时间"); return; }
  const fmt = String(h).padStart(2, "0") + ":00";
  try {
    await api("/api/config/checkin_times", { times: [fmt] });
    toast("签到时间已设为 " + fmt);
    loadState();
  } catch (e) { toast(e.message); }
}

function renderTimes() {
  const times = state.checkin_times || [];
  // 填充签到时间下拉
  const selCk = $("checkinTime");
  if (selCk) {
    const cur = times.length > 0 ? parseInt(times[0], 10) : 9;
    selCk.innerHTML = "";
    for (let h = 0; h <= 23; h++) {
      const opt = document.createElement("option");
      opt.value = h;
      opt.textContent = String(h).padStart(2, "0") + ":00";
      if (h === cur) opt.selected = true;
      selCk.appendChild(opt);
    }
  }
  $("nextCheckin").textContent = state.next_checkin || "-";
  $("nextKeepalive").textContent = state.next_keepalive || "-";
  // 填充保活小时下拉
  const sel = $("keepaliveHour");
  if (sel) {
    const cur = state.keepalive_hours && state.keepalive_hours.length > 0 ? state.keepalive_hours[0] : 22;
    sel.innerHTML = "";
    for (let h = 0; h <= 23; h++) {
      const opt = document.createElement("option");
      opt.value = h;
      opt.textContent = String(h).padStart(2, "0") + ":00";
      if (h === cur) opt.selected = true;
      sel.appendChild(opt);
    }
  }
}

async function saveKeepalive() {
  const sel = $("keepaliveHour");
  if (!sel) return;
  const h = parseInt(sel.value, 10);
  if (isNaN(h) || h < 0 || h > 23) { toast("无效保活小时"); return; }
  try {
    await api("/api/config/keepalive_hours", { hours: [h] });
    toast("保活时间已设为 " + String(h).padStart(2, "0") + ":00");
    loadState();
  } catch (e) { toast(e.message); }
}

// ---------- 开机自启 ----------
async function toggleAutostart() {
  try {
    await api("/api/config/autostart", { on: $("chkAutostart").checked });
    toast("设置已保存");
  } catch (e) { toast(e.message); loadState(); }
}

// ---------- API 配置弹层 ----------
function openApiConfig() {
  $("inPort").value = state.listen_port;
  $("selHost").value = state.listen_host === "127.0.0.1" ? "127.0.0.1"
    : (state.listen_host === "0.0.0.0" || state.listen_host === "" || state.listen_host === "::") ? "0.0.0.0"
    : "__custom__";
  if ($("selHost").value === "__custom__") {
    $("inHost").value = state.listen_host;
    $("customHostRow").classList.remove("hidden");
  } else {
    $("customHostRow").classList.add("hidden");
  }
  // 模型路由
  const cc = state.compat || {};
  const channels = cc.channels || [];
  $("selCh").innerHTML = channels.map(c => `<option value="${c}">${c}</option>`).join("");
  $("selCh").value = cc.default_channel || (channels[0] || "");
  $("inMaxTok").value = cc.max_tokens_cap || 0;
  const map = cc.model_map || {};
  const entries = Object.entries(map);
  $("mapSummary").textContent = entries.length === 0 ? "（空）" : entries.map(([k,v]) => `${k} → ${v}`).join("\u00A0 \u00A0");
  // 映射编辑器：随对话框打开而重置为当前值，收起
  $("mapText").value = entries.map(([k,v]) => `${k} = ${v}`).join("\n");
  $("mapEditor").classList.add("hidden");
  $("mapErr").textContent = "";
  renderMapPresets(channels);
  $("apiConfigOverlay").classList.remove("hidden");
}

function closeApiConfig() {
  $("apiConfigOverlay").classList.add("hidden");
}

// ---------- 模型映射编辑器 ----------

// CHANNEL_PRESETS 各渠道的缺省建议映射（模型为该渠道常用/免费模型）。
// 用途：未绑定某渠道账号、或不知道该渠道有哪些模型时，给出可点选的起点。
const CHANNEL_PRESETS = {
  workbuddy:   { label: "Claude Code → workbuddy",  items: ["claude-* = workbuddy/glm-5.2", "claude-sonnet-* = workbuddy/kimi-k2.7"] },
  traework:    { label: "Codex → traework",          items: ["gpt-5* = traework/glm-5.2", "codex-* = traework/DeepSeek-V4-Pro"] },
  workbuddyai: { label: "Claude Code → workbuddyai", items: ["claude-* = workbuddyai/deepseek-v4.1-flash"] },
  qodercn:     { label: "→ qodercn",                 items: ["gpt-* = qodercn/glm-5.3"] },
  qodercom:    { label: "→ qodercom",                items: ["gpt-* = qodercom/glm-5.3"] },
  qwenwork:    { label: "→ qwenwork",                items: ["gpt-* = qwenwork/flash", "claude-* = qwenwork/pro"] },
};

// renderMapPresets 按当前已接入渠道渲染缺省建议按钮。
// 仅列出「已接入（有账号）」的渠道——未绑定的渠道点了也会因无账号而失败，不给误导性入口。
function renderMapPresets(channels) {
  const bound = new Set((state.accounts || []).map(a => a.group));
  const box = $("mapPresets");
  box.innerHTML = channels.filter(c => CHANNEL_PRESETS[c] && bound.has(c)).map(c => {
    const p = CHANNEL_PRESETS[c];
    return `<span class="btn tiny preset" data-ch="${c}" title="${esc(p.items.join("\n"))}">${esc(p.label)}</span>`;
  }).join(" ") || `<span class="hint">（尚未接入任何渠道，先在账号管理添加账号）</span>`;
  box.querySelectorAll(".preset").forEach(b => {
    b.onclick = () => {
      const p = CHANNEL_PRESETS[b.dataset.ch];
      // 追加尚未存在的行，避免重复插入
      const cur = $("mapText").value.split(/\r?\n/).map(s => s.trim()).filter(Boolean);
      const have = new Set(cur.map(l => l.split("=")[0].trim()));
      const add = p.items.filter(it => !have.has(it.split("=")[0].trim()));
      if (!add.length) { toast(`${p.label} 的建议映射已存在`); return; }
      $("mapText").value = cur.concat(add).join("\n");
    };
  });
}

// toggleMapEditor 展开/收起映射编辑器。
function toggleMapEditor() {
  $("mapEditor").classList.toggle("hidden");
}

// parseMapText 校验编辑器内容，返回 (modelMap, 错误信息)。
// 语法错误就地提示并阻断保存，不再弹 prompt 循环。
function parseMapText(raw) {
  const map = {};
  const channels = new Set(state.compat?.channels || []);
  for (const line0 of raw.split(/\r?\n/)) {
    const line = line0.trim();
    if (!line || line.startsWith("#")) continue;
    const idx = line.indexOf("=");
    if (idx < 0) return [null, `格式错误（缺少 =）：${line}`];
    const k = line.substring(0, idx).trim(), v = line.substring(idx + 1).trim();
    if (!k || !v) return [null, `格式错误（键或值为空）：${line}`];
    const vi = v.indexOf("/");
    if (vi <= 0 || !v.substring(vi + 1).trim()) return [null, `映射目标必须是「渠道/模型」形式：${v}`];
    const ch = v.substring(0, vi);
    if (channels.size && !channels.has(ch)) {
      return [null, `未知渠道「${ch}」；已知渠道：${[...channels].join(", ")}`];
    }
    if (map[k] !== undefined) return [null, `重复的键：${k}`];
    map[k] = v;
  }
  return [map, ""];
}

async function saveApiConfig() {
  let host = $("selHost").value;
  if (host === "__custom__") host = $("inHost").value.trim() || "127.0.0.1";
  const port = parseInt($("inPort").value, 10);
  try {
    await api("/api/config/listen", { host, port });
  } catch (e) { toast(e.message); return; }

  // 映射：优先读编辑器；编辑器从未展开过则用原值（保证「只改监听/渠道不碰映射」）
  let modelMap = state.compat?.model_map || {};
  if (!$("mapEditor").classList.contains("hidden")) {
    const [parsed, err] = parseMapText($("mapText").value);
    if (err) { $("mapErr").textContent = err; toast(err); return; }
    modelMap = parsed;
  }

  const defaultChannel = $("selCh").value;
  const maxTokensCap = parseInt($("inMaxTok").value, 10) || 0;
  try {
    await api("/api/config/compat", { default_channel: defaultChannel, max_tokens_cap: maxTokensCap, model_map: modelMap });
    toast("模型路由已更新");
    closeApiConfig();
    loadState();
  } catch (e) { toast(e.message); }
}

// ---------- API Key 弹层 ----------
let apiKeyMode = "v1"; // "v1" or "v2"
function openApiKey() {
  apiKeyMode = "v1";
  $("apiKeyOverlay").querySelector(".modal-title").textContent = "修改 API-Key";
  $("keyInput").value = state.api_key;
  $("apiKeyOverlay").classList.remove("hidden");
  $("keyInput").focus();
}
function openApiKeyV2() {
  apiKeyMode = "v2";
  $("apiKeyOverlay").querySelector(".modal-title").textContent = "修改 v2 API-Key";
  $("keyInput").value = state.api_key_v2 || "";
  $("apiKeyOverlay").classList.remove("hidden");
  $("keyInput").focus();
}

function closeApiKey() {
  $("apiKeyOverlay").classList.add("hidden");
}

function generateKey() {
  const chars = "abcdefghijklmnopqrstuvwxyz0123456789";
  let key = "sk-";
  for (let i = 0; i < 32; i++) {
    key += chars[Math.floor(Math.random() * chars.length)];
  }
  $("keyInput").value = key;
}

async function saveApiKey() {
  const key = $("keyInput").value.trim();
  try {
    if (apiKeyMode === "v2") {
      await api("/api/config/api_key_v2", { key });
    } else {
      await api("/api/config/api_key", { key });
    }
    closeApiKey();
    toast(apiKeyMode === "v2" ? "v2 API-Key 已更新" : "API-Key 已更新");
    loadState();
  } catch (e) { toast(e.message); }
}

// ---------- 复制到剪贴板 ----------
async function copyText(text, label) {
  try {
    await navigator.clipboard.writeText(text);
    toast(`${label}已复制到剪贴板`);
  } catch (e) {
    // 降级方案：execCommand
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand("copy");
      toast(`${label}已复制到剪贴板`);
    } catch (err) {
      toast("复制失败，请手动复制");
    }
    document.body.removeChild(ta);
  }
}
function openHelp() { $("helpOverlay").classList.remove("hidden"); }
function closeHelp() { $("helpOverlay").classList.add("hidden"); }
function openAbout() { $("aboutOverlay").classList.remove("hidden"); }
function closeAbout() { $("aboutOverlay").classList.add("hidden"); }

// ---------- 通用确认框 ----------
function confirmDialog(msg, onOk) {
  $("confirmMsg").textContent = msg;
  $("confirmOverlay").classList.remove("hidden");
  $("btnConfirmOk").onclick = () => { $("confirmOverlay").classList.add("hidden"); onOk(); };
  $("btnConfirmCancel").onclick = () => $("confirmOverlay").classList.add("hidden");
}

// ---------- 事件绑定 ----------
function bind() {
  $("btnAddWB").onclick = () => promptLogin("workbuddy");
  $("btnAddWBAI").onclick = () => promptLogin("workbuddyai");
  $("btnAddTrae").onclick = () => promptLogin("traework");
  $("btnAddQoderCN").onclick = () => promptLogin("qodercn");
  $("btnAddQoderCOM").onclick = () => promptLogin("qodercom");
  $("btnAddQwen").onclick = () => promptLogin("qwenwork");
  $("btnCheckinAll").onclick = checkinAll;
  $("btnRefreshAll").onclick = refreshAll;
  $("btnCopyUrl").onclick = copyUrl;
  $("btnCancelLogin").onclick = cancelLogin;
  $("btnRefreshFees").onclick = refreshFees;
    $("btnBenchmark").onclick = runBenchmark;
    $("chkAutostart").onchange = toggleAutostart;

  $("btnSaveCheckinTime").onclick = saveCheckinTime;
  $("btnSaveKeepalive").onclick = saveKeepalive;

  $("apiAddr").onclick = () => {
      const v = $("apiAddr").querySelector(".val").textContent;
      copyText(v, "OpenAI 接口地址");
    };
    $("apiKeyDisplay").onclick = () => {
      const v = $("apiKeyDisplay").querySelector(".val").textContent;
      if (v === "（无鉴权）") { toast("当前未设置 API-Key"); return; }
      copyText(v, "API-Key");
    };
    $("apiAddrV2").onclick = () => {
      const v = $("apiAddrV2").querySelector(".val").textContent;
      copyText(v, "v2 接口地址");
    };
    $("apiKeyDisplayV2").onclick = () => {
      const v = $("apiKeyDisplayV2").querySelector(".val").textContent;
      if (v === "（无鉴权）") { toast("当前未设置 v2 API-Key"); return; }
      copyText(v, "v2 API-Key");
    };
    // 修改图标点击弹配置对话框（不触发复制）
    document.querySelectorAll(".icon-edit")[0].onclick = openApiConfig;
    document.querySelectorAll(".icon-edit")[1].onclick = openApiKey;
    document.querySelectorAll(".icon-edit")[2].onclick = openApiKeyV2;

  $("btnHelp").onclick = openHelp;
  $("btnAbout").onclick = openAbout;
  $("btnHelpClose").onclick = closeHelp;
  $("btnAboutClose").onclick = closeAbout;

  // 登录确认弹层
  $("btnLoginConfirm").onclick = confirmLogin;
  $("btnLoginConfirmCancel").onclick = () => $("loginConfirmOverlay").classList.add("hidden");
  $("loginConfirmOverlay").onclick = (e) => { if (e.target === $("loginConfirmOverlay")) $("loginConfirmOverlay").classList.add("hidden"); };

  $("btnApiSave").onclick = saveApiConfig;
  $("btnApiCancel").onclick = closeApiConfig;
  $("btnCompatMap").onclick = toggleMapEditor;
  $("selHost").onchange = () => {
    $("customHostRow").classList.toggle("hidden", $("selHost").value !== "__custom__");
  };

  $("btnKeySave").onclick = saveApiKey;
    $("btnKeyCancel").onclick = closeApiKey;
    $("btnKeyGenerate").onclick = generateKey;
    $("keyInput").addEventListener("keydown", (e) => { if (e.key === "Enter") saveApiKey(); });

  // 点击弹层空白处关闭
  $("apiConfigOverlay").onclick = (e) => { if (e.target === $("apiConfigOverlay")) closeApiConfig(); };
  $("apiKeyOverlay").onclick = (e) => { if (e.target === $("apiKeyOverlay")) closeApiKey(); };
  $("helpOverlay").onclick = (e) => { if (e.target === $("helpOverlay")) closeHelp(); };
  $("aboutOverlay").onclick = (e) => { if (e.target === $("aboutOverlay")) closeAbout(); };
}

// ---------- 初始化 ----------
(async function init() {
  bind();
  try {
    await loadState();
    await loadFees();
  } catch (e) {
    toast("无法连接后台服务：" + e.message);
  }
})();