"use strict";

// ---------- 基础工具 ----------
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

async function api(method, path, body) {
  const opt = { method, headers: {}, credentials: "same-origin" };
  if (body !== undefined) {
    opt.headers["Content-Type"] = "application/json";
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(path, opt);
  let data = null;
  const text = await res.text();
  if (text) { try { data = JSON.parse(text); } catch { data = { raw: text }; } }
  if (res.status === 401 && data && data.error) {
    showLogin();
    throw new Error(data.error);
  }
  if (!res.ok) throw new Error((data && data.error) || `HTTP ${res.status}`);
  return data;
}

let toastTimer = null;
function toast(msg, isErr) {
  const el = $("#toast");
  el.textContent = msg;
  el.className = isErr ? "err" : "";
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.add("hidden"), 2600);
}

const fmtNum = (n) => (n == null ? "-" : Number(n).toLocaleString("en-US"));
const fmtCost = (n) => "$" + (Number(n) || 0).toFixed(4);
const fmtTime = (unix) => (unix ? new Date(unix * 1000).toLocaleString("zh-CN") : "-");
function fmtTokens(n) {
  n = Number(n) || 0;
  if (n >= 1e6) return (n / 1e6).toFixed(2) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "k";
  return String(n);
}
function el(tag, attrs = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "class") e.className = v;
    else if (k === "html") e.innerHTML = v;
    else if (k === "text") e.textContent = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (v !== null && v !== undefined) e.setAttribute(k, v);
  }
  for (const k of kids) e.append(k);
  return e;
}

// ---------- 弹窗 ----------
function openModal(title, bodyNode) {
  $("#modal-title").textContent = title;
  const body = $("#modal-body");
  body.replaceChildren(bodyNode);
  $("#modal-mask").classList.remove("hidden");
}
function closeModal() { $("#modal-mask").classList.add("hidden"); }
$("#modal-close").onclick = closeModal;
$("#modal-mask").onclick = (e) => { if (e.target.id === "modal-mask") closeModal(); };

// ---------- 登录 ----------
function showLogin() {
  $("#login-view").classList.remove("hidden");
  $("#app-view").classList.add("hidden");
}
function showApp(authDisabled) {
  $("#login-view").classList.add("hidden");
  $("#app-view").classList.remove("hidden");
  $("#logout-btn").classList.toggle("hidden", authDisabled);
  $("#auth-badge").textContent = authDisabled ? "免密模式（仅本机）" : "";
  loadDashboard();
}

$("#login-form").onsubmit = async (e) => {
  e.preventDefault();
  $("#login-error").textContent = "";
  try {
    const r = await api("POST", "/admin/api/login", { password: $("#login-password").value });
    showApp(!!r.auth_disabled);
  } catch (err) {
    $("#login-error").textContent = err.message;
  }
};
$("#logout-btn").onclick = async () => {
  try { await api("POST", "/admin/api/logout"); } catch {}
  showLogin();
};

// ---------- 标签页 ----------
const loaders = {};
$$(".tab").forEach((btn) => {
  btn.onclick = () => {
    $$(".tab").forEach((b) => b.classList.toggle("active", b === btn));
    $$(".tabpane").forEach((p) => p.classList.toggle("hidden", p.id !== "tab-" + btn.dataset.tab));
    const fn = loaders[btn.dataset.tab];
    if (fn) fn();
  };
});
function switchTab(name) {
  const btn = $(`.tab[data-tab="${name}"]`);
  if (btn) btn.click();
}

// ---------- 仪表盘 ----------
loaders.dashboard = loadDashboard;
async function loadDashboard() {
  let d;
  try { d = await api("GET", "/admin/api/summary"); }
  catch (e) { toast(e.message, true); return; }
  const a = d.accounts || {};
  const u24 = d.usage_24h || {}, u7 = d.usage_7d || {};
  const cards = [
    { lbl: "账号可用 / 总数", num: `${a.usable || 0} / ${a.total || 0}`, cls: a.usable ? "ok" : "bad" },
    { lbl: "冷却中", num: a.cooldown || 0, cls: a.cooldown ? "warn" : "" },
    { lbl: "额度耗尽", num: a.exhausted || 0, cls: a.exhausted ? "warn" : "" },
    { lbl: "已挂起 / 停用", num: `${a.suspended || 0} / ${a.disabled || 0}` },
    { lbl: "24h 请求数", num: fmtNum(u24.requests) },
    { lbl: "24h Token", num: fmtTokens((u24.input_tokens || 0) + (u24.output_tokens || 0)) },
    { lbl: "24h 费用", num: fmtCost(u24.cost) },
    { lbl: "7d 费用", num: fmtCost(u7.cost) },
  ];
  $("#dash-cards").replaceChildren(...cards.map((c) =>
    el("div", { class: "card " + (c.cls || "") },
      el("div", { class: "lbl", text: c.lbl }),
      el("div", { class: "num", text: String(c.num) }))));

  const tbody = $("#dash-top-models tbody");
  const rows = (d.top_models || []).map((m) =>
    el("tr", {},
      el("td", { class: "mono", text: m.model }),
      el("td", { text: fmtNum(m.requests) }),
      el("td", { text: fmtTokens(m.input_tokens) }),
      el("td", { text: fmtTokens(m.output_tokens) }),
      el("td", { text: fmtTokens(m.cached_tokens) }),
      el("td", { text: fmtCost(m.cost) })));
  tbody.replaceChildren(...(rows.length ? rows : [el("tr", {}, el("td", { colspan: "6", class: "muted", text: "暂无数据" }))]));
}

// ---------- 账号池 ----------
loaders.accounts = loadAccounts;
async function loadAccounts() {
  let d;
  try { d = await api("GET", "/admin/api/accounts"); }
  catch (e) { toast(e.message, true); return; }
  const tbody = $("#accounts-table tbody");
  const rows = (d.items || []).map(accountRow);
  tbody.replaceChildren(...(rows.length ? rows : [el("tr", {}, el("td", { colspan: "8", class: "muted", text: "还没有账号，点右上角添加" }))]));
}

function accountStatus(a) {
  if (!a.enabled) return { cls: "off", text: "已停用" };
  if (a.cooldown) return { cls: "warn", text: `冷却 ${a.cooldown_seconds}s` };
  if (a.suspended) return { cls: "bad", text: "已挂起" };
  if (a.used_ratio >= 1) return { cls: "bad", text: "额度耗尽" };
  return { cls: "ok", text: "可用" };
}

function accountRow(a) {
  const st = accountStatus(a);
  const pct = Math.min(100, Math.round((a.used_ratio || 0) * 100));
  return el("tr", {},
    el("td", {}, el("div", { text: a.email || a.id }), a.name && a.name !== a.email ? el("div", { class: "muted", text: a.name }) : null),
    el("td", {}, a.plan ? el("span", { class: "pill", text: a.plan }) : el("span", { class: "muted", text: "-" }),
      a.plan_exp ? el("div", { class: "muted", text: fmtTime(a.plan_exp).slice(0, 10) }) : null),
    el("td", {}, el("span", { class: "dot " + st.cls }), st.text),
    el("td", { text: a.limits_fetched ? pct + "%" : "未拉取" }),
    el("td", { text: a.inflight }),
    el("td", { text: a.failures || 0 }),
    el("td", { class: "wrap muted", text: a.last_error || "" }),
    el("td", {},
      el("button", { class: "btn sm", onclick: () => refreshAccount(a.id) }, "刷新额度"),
      " ",
      el("button", { class: "btn sm", onclick: () => toggleAccount(a) }, a.enabled ? "停用" : "启用"),
      " ",
      el("button", { class: "btn sm danger", onclick: () => deleteAccount(a) }, "删除")));
}

async function refreshAccount(id) {
  try { await api("POST", `/admin/api/accounts/${id}/refresh`); toast("额度已刷新"); loadAccounts(); }
  catch (e) { toast(e.message, true); }
}
async function toggleAccount(a) {
  try { await api("PATCH", `/admin/api/accounts/${a.id}`, { enabled: !a.enabled }); loadAccounts(); }
  catch (e) { toast(e.message, true); }
}
async function deleteAccount(a) {
  if (!confirm(`确认删除账号 ${a.email || a.id}？该账号的 refresh token 将被移除。`)) return;
  try { await api("DELETE", `/admin/api/accounts/${a.id}`); toast("已删除"); loadAccounts(); }
  catch (e) { toast(e.message, true); }
}

// 添加账号：OAuth
$("#acct-add-oauth").onclick = async () => {
  let providers;
  try { providers = await api("GET", "/admin/api/accounts/oauth/providers"); }
  catch (e) { toast(e.message, true); return; }
  const list = (providers.providers || providers.items || providers || []);
  const names = (Array.isArray(list) ? list : []).map((p) => (typeof p === "string" ? p : p.name || p.id || p.provider))
    .filter(Boolean);
  const opts = names.length ? names : ["github", "google"];
  const sel = el("select", {}, ...opts.map((n) => el("option", { value: n, text: n })));
  const mode = el("select", {},
    el("option", { value: "auto", text: "自动回调（本机 127.0.0.1 独占端口）" }),
    el("option", { value: "manual", text: "手动粘贴回调链接" }));
  const wrap = el("div", {},
    el("div", { class: "row" }, el("label", { text: "登录提供商" }), sel),
    el("div", { class: "row" }, el("label", { text: "回调方式" }), mode),
    el("div", { class: "modal-actions" }, el("button", { class: "btn primary", onclick: startOAuth }, "开始登录")));
  openModal("OAuth 添加账号", wrap);

  async function startOAuth() {
    let r;
    try { r = await api("POST", "/admin/api/accounts/oauth/start", { provider: sel.value, mode: mode.value }); }
    catch (e) { toast(e.message, true); return; }
    const link = el("a", { href: r.auth_url, target: "_blank", text: r.auth_url, class: "mono" });
    const statusEl = el("div", { class: "muted", text: r.mode === "auto" ? "等待浏览器完成授权…" : "完成授权后，复制浏览器地址栏的完整链接粘贴到下方。" });
    const body = el("div", {},
      el("div", { class: "row" }, el("label", { text: "1. 在浏览器打开该链接完成授权" }), link),
      el("div", { class: "row" }, el("label", { text: "2. 状态" }), statusEl));
    if (r.mode === "manual") {
      const ta = el("textarea", { rows: "3", placeholder: "http://127.0.0.1:9/callback?refresh_token=..." });
      body.append(el("div", { class: "row" }, el("label", { text: "回调链接" }), ta,
        el("div", { class: "modal-actions" }, el("button", {
          class: "btn primary",
          onclick: async () => {
            try {
              const rr = await api("POST", "/admin/api/accounts/oauth/complete", { callback_url: ta.value, provider: r.provider });
              toast("账号已加入：" + (rr.account ? rr.account.email : ""));
              closeModal(); loadAccounts(); switchTab("accounts");
            } catch (e) { toast(e.message, true); }
          },
        }, "提交"))));
      openModal("OAuth 添加账号", body);
      window.open(r.auth_url, "_blank");
      return;
    }
    openModal("OAuth 添加账号", body);
    window.open(r.auth_url, "_blank");
    // 轮询 auto 流程结果
    const timer = setInterval(async () => {
      try {
        const rr = await api("POST", "/admin/api/accounts/oauth/complete", { flow_id: r.flow_id });
        if (rr.status === "done") {
          clearInterval(timer);
          toast("账号已加入：" + (rr.account ? rr.account.email : ""));
          closeModal(); loadAccounts(); switchTab("accounts");
        } else if (rr.status === "error") {
          clearInterval(timer);
          statusEl.textContent = "失败：" + rr.error;
          statusEl.className = "error";
        }
      } catch (e) { /* 网络抖动，继续轮询 */ }
    }, 2000);
    setTimeout(() => clearInterval(timer), 10 * 60 * 1000);
  }
};

// 添加账号：邮箱验证码
$("#acct-add-email").onclick = () => {
  const email = el("input", { placeholder: "you@example.com" });
  const code = el("input", { placeholder: "6 位验证码" });
  const sendBtn = el("button", { class: "btn", onclick: send }, "发送验证码");
  const wrap = el("div", {},
    el("div", { class: "row" }, el("label", { text: "邮箱" }), email),
    el("div", { class: "row" }, el("label", { text: "验证码" }), code, el("div", { class: "modal-actions" }, sendBtn)),
    el("div", { class: "modal-actions" }, el("button", { class: "btn primary", onclick: submit }, "验证并添加")));
  openModal("邮箱验证码添加账号", wrap);

  async function send() {
    try {
      const r = await api("POST", "/admin/api/accounts/email/start", { email: email.value });
      toast(r.dev_code ? "开发验证码: " + r.dev_code : "验证码已发送");
    } catch (e) { toast(e.message, true); }
  }
  async function submit() {
    try {
      const r = await api("POST", "/admin/api/accounts/email/complete", { email: email.value, code: code.value });
      toast("账号已加入：" + (r.account ? r.account.email : ""));
      closeModal(); loadAccounts(); switchTab("accounts");
    } catch (e) { toast(e.message, true); }
  }
};

// 添加账号：粘贴 refresh token
$("#acct-add-token").onclick = () => {
  const tok = el("textarea", { rows: "5", placeholder: "粘贴 refresh token（三段 JWT，有效期 30 天）" });
  const provider = el("input", { value: "manual", placeholder: "provider（可空）" });
  const wrap = el("div", {},
    el("div", { class: "row" }, el("label", { text: "Refresh Token" }), tok),
    el("div", { class: "row" }, el("label", { text: "来源标记（可选）" }), provider),
    el("div", { class: "modal-actions" }, el("button", { class: "btn primary", onclick: submit }, "添加")));
  openModal("粘贴 Token 添加账号", wrap);
  async function submit() {
    try {
      const r = await api("POST", "/admin/api/accounts/oauth/complete", { token: tok.value.trim(), provider: provider.value.trim() });
      toast("账号已加入：" + (r.account ? r.account.email : ""));
      closeModal(); loadAccounts(); switchTab("accounts");
    } catch (e) { toast(e.message, true); }
  }
};

// ---------- API Keys ----------
loaders.keys = loadKeys;
async function loadKeys() {
  let d;
  try { d = await api("GET", "/admin/api/keys"); }
  catch (e) { toast(e.message, true); return; }
  const tbody = $("#keys-table tbody");
  const rows = (d.items || []).map((k) => {
    const expired = k.expires_at && k.expires_at * 1000 < Date.now();
    const st = !k.enabled ? { cls: "off", text: "已停用" } : expired ? { cls: "bad", text: "已过期" } : { cls: "ok", text: "启用" };
    return el("tr", {},
      el("td", { text: k.name }),
      el("td", { class: "mono", text: k.key_display }),
      el("td", {}, el("span", { class: "dot " + st.cls }), st.text),
      el("td", { text: k.concurrency || "默认" }),
      el("td", { text: k.rate_limit_rpm || "不限" }),
      el("td", { class: "wrap", text: (k.model_allowlist && k.model_allowlist.length) ? k.model_allowlist.join(", ") : "全部" }),
      el("td", { text: fmtCost(k.total_cost) }),
      el("td", {},
        el("button", { class: "btn sm", onclick: () => editKey(k) }, "编辑"),
        " ",
        el("button", { class: "btn sm danger", onclick: () => deleteKey(k) }, "删除")));
  });
  tbody.replaceChildren(...(rows.length ? rows : [el("tr", {}, el("td", { colspan: "8", class: "muted", text: "还没有 API Key" }))]));
}

$("#key-create-btn").onclick = () => {
  const name = el("input", { placeholder: "例如：my-codex" });
  const conc = el("input", { type: "number", min: "0", placeholder: "0 = 默认 5" });
  const rpm = el("input", { type: "number", min: "0", placeholder: "0 = 不限" });
  const models = el("input", { placeholder: "逗号分隔，空=全部模型" });
  const expires = el("input", { type: "date" });
  const wrap = el("div", {},
    el("div", { class: "row" }, el("label", { text: "名称" }), name),
    el("div", { class: "row" }, el("label", { text: "并发上限" }), conc),
    el("div", { class: "row" }, el("label", { text: "每分钟请求数（RPM）" }), rpm),
    el("div", { class: "row" }, el("label", { text: "模型白名单" }), models),
    el("div", { class: "row" }, el("label", { text: "过期时间（可选）" }), expires),
    el("div", { class: "modal-actions" }, el("button", { class: "btn primary", onclick: submit }, "创建")));
  openModal("新建 API Key", wrap);

  async function submit() {
    const body = { name: name.value.trim() };
    if (conc.value !== "") body.concurrency = Number(conc.value);
    if (rpm.value !== "") body.rate_limit_rpm = Number(rpm.value);
    if (models.value.trim()) body.model_allowlist = models.value.split(",").map((s) => s.trim()).filter(Boolean);
    if (expires.value) body.expires_at = Math.floor(new Date(expires.value + "T23:59:59").getTime() / 1000);
    let r;
    try { r = await api("POST", "/admin/api/keys", body); }
    catch (e) { toast(e.message, true); return; }
    const keyBox = el("input", { value: r.key, readonly: "", class: "mono" });
    const copyBtn = el("button", {
      class: "btn", onclick: () => { keyBox.select(); document.execCommand("copy"); toast("已复制"); },
    }, "复制");
    const body2 = el("div", {},
      el("p", { class: "error", text: "⚠ " + r.warning }),
      el("div", { class: "row" }, el("label", { text: "明文密钥" }), keyBox),
      el("div", { class: "modal-actions" }, copyBtn, el("button", { class: "btn primary", onclick: () => { closeModal(); loadKeys(); } }, "我已保存")));
    openModal("API Key 创建成功", body2);
  }
};

function editKey(k) {
  const name = el("input", { value: k.name });
  const conc = el("input", { type: "number", min: "0", value: k.concurrency || 0 });
  const rpm = el("input", { type: "number", min: "0", value: k.rate_limit_rpm || 0 });
  const models = el("input", { value: (k.model_allowlist || []).join(", "), placeholder: "空=全部模型" });
  const enabled = el("select", {},
    el("option", { value: "1", text: "启用", selected: k.enabled ? "" : null }),
    el("option", { value: "0", text: "停用", selected: k.enabled ? null : "" }));
  const wrap = el("div", {},
    el("div", { class: "row" }, el("label", { text: "名称" }), name),
    el("div", { class: "row" }, el("label", { text: "状态" }), enabled),
    el("div", { class: "row" }, el("label", { text: "并发上限" }), conc),
    el("div", { class: "row" }, el("label", { text: "RPM 上限" }), rpm),
    el("div", { class: "row" }, el("label", { text: "模型白名单" }), models),
    el("div", { class: "modal-actions" }, el("button", { class: "btn primary", onclick: submit }, "保存")));
  openModal("编辑 API Key", wrap);
  async function submit() {
    const body = {
      name: name.value.trim(),
      enabled: enabled.value === "1",
      concurrency: Number(conc.value) || 0,
      rate_limit_rpm: Number(rpm.value) || 0,
      model_allowlist: models.value.trim() ? models.value.split(",").map((s) => s.trim()).filter(Boolean) : [],
    };
    try { await api("PATCH", `/admin/api/keys/${k.id}`, body); toast("已保存"); closeModal(); loadKeys(); }
    catch (e) { toast(e.message, true); }
  }
}

async function deleteKey(k) {
  if (!confirm(`确认删除 API Key「${k.name}」？使用该 Key 的客户端将立即失效。`)) return;
  try { await api("DELETE", `/admin/api/keys/${k.id}`); toast("已删除"); loadKeys(); }
  catch (e) { toast(e.message, true); }
}

// ---------- 用量日志 ----------
loaders.usage = loadUsage;
let usageOffset = 0;
const usageLimit = 50;

async function loadUsage(reset) {
  if (reset) usageOffset = 0;
  const params = new URLSearchParams({ limit: String(usageLimit), offset: String(usageOffset) });
  const since = $("#usage-since").value;
  if (since) params.set("since", since);
  const model = $("#usage-model").value.trim();
  if (model) params.set("model", model);
  let d;
  try { d = await api("GET", "/admin/api/usage/logs?" + params); }
  catch (e) { toast(e.message, true); return; }
  const tbody = $("#usage-table tbody");
  const rows = (d.items || []).map((l) => el("tr", {},
    el("td", { text: fmtTime(l.created_at) }),
    el("td", { class: "mono", text: l.model }),
    el("td", { class: "mono", text: l.endpoint }),
    el("td", { text: fmtTokens(l.input_tokens) }),
    el("td", { text: fmtTokens(l.output_tokens) }),
    el("td", { text: fmtTokens(l.cached_tokens) }),
    el("td", { text: fmtCost(l.cost) }),
    el("td", {}, el("span", { class: "dot " + (l.status < 400 ? "ok" : "bad") }), String(l.status), l.err ? el("span", { class: "muted", text: " " + l.err }) : null),
    el("td", { text: l.duration_ms + "ms" })));
  tbody.replaceChildren(...(rows.length ? rows : [el("tr", {}, el("td", { colspan: "9", class: "muted", text: "暂无日志" }))]));
  const from = d.total === 0 ? 0 : usageOffset + 1;
  $("#usage-page-info").textContent = `${from}–${usageOffset + rows.length} / 共 ${d.total} 条`;
  $("#usage-prev").disabled = usageOffset === 0;
  $("#usage-next").disabled = usageOffset + usageLimit >= d.total;
  if (reset) usageOffset = 0;
}
$("#usage-reload").onclick = () => loadUsage(true);
$("#usage-since").onchange = () => loadUsage(true);
$("#usage-model").onchange = () => loadUsage(true);
$("#usage-prev").onclick = () => { usageOffset = Math.max(0, usageOffset - usageLimit); loadUsage(); };
$("#usage-next").onclick = () => { usageOffset += usageLimit; loadUsage(); };

// ---------- 设置 ----------
loaders.settings = loadSettings;
let settingItems = [];

async function loadSettings() {
  let d;
  try { d = await api("GET", "/admin/api/settings"); }
  catch (e) { toast(e.message, true); return; }
  settingItems = d.settings || [];
  renderSettings();
}

const settingLabels = {
  upstream_proxy: "出站代理（HTTP CONNECT）",
  claude_cloak_mode: "Claude 伪装模式（relaxed/strict）",
  capacity_retries: "503 容量重试次数",
  capacity_backoff_ms: "容量重试退避（毫秒）",
  model_prices: "模型价格表（JSON）",
  rate_multiplier: "计费倍率",
};
const settingHints = {
  upstream_proxy: "例如 http://127.0.0.1:7890，留空=直连",
  claude_cloak_mode: "relaxed=指纹+原文（超 200 字节截断）；strict=仅身份行",
  model_prices: '如 {"claude-sonnet-5":{"input":3,"output":15,"cached":0.3}}',
  rate_multiplier: "默认 1，费用 = 价格 × 倍率",
};

function renderSettings() {
  const list = el("div", {});
  for (const s of settingItems) {
    const isTextarea = s.key === "model_prices";
    const input = isTextarea
      ? el("textarea", { rows: "4", class: "mono", "data-key": s.key })
      : el("input", { "data-key": s.key, placeholder: settingHints[s.key] || "" });
    input.value = s.value || "";
    const src = el("div", { class: "src " + s.source, text: { database: "数据库", env: "环境变量", default: "默认值" }[s.source] || s.source });
    list.append(el("div", { class: "setting-row" },
      el("div", {}, el("div", { class: "k", text: settingLabels[s.key] || s.key }),
        el("div", { class: "muted", text: s.key, style: "font-size:11px" })),
      input, src));
  }
  $("#settings-list").replaceChildren(list);
}

$("#settings-save").onclick = async () => {
  const body = {};
  $$("#settings-list [data-key]").forEach((i) => { body[i.dataset.key] = i.value; });
  try { await api("PUT", "/admin/api/settings", body); toast("已保存"); loadSettings(); }
  catch (e) { toast(e.message, true); }
};

// ---------- 启动 ----------
(function boot() {
  api("GET", "/admin/api/session")
    .then((s) => (s.authenticated ? showApp(!!s.auth_disabled) : showLogin()))
    .catch(() => showLogin());
})();
