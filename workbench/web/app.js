import {builtinModels, endpointFor, makePayload, eventText, consumeSSE} from './protocol.mjs';
import {goModels, quotaWindow, allowedModelList} from './quota.mjs';

const $ = s => document.querySelector(s), $$ = s => [...document.querySelectorAll(s)];
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const icons = {
  grid:'<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  users:'<circle cx="9" cy="8" r="3"/><path d="M3 21v-3a6 6 0 0 1 12 0v3M16 5a3 3 0 0 1 0 6M18 15a5 5 0 0 1 3 5"/>',
  key:'<circle cx="8" cy="15" r="5"/><path d="m12 11 9-9m-5 5 3 3m-1-5 3 3"/>',
  cpu:'<rect x="5" y="5" width="14" height="14" rx="3"/><rect x="9" y="9" width="6" height="6" rx="1"/><path d="M9 2v3m6-3v3M9 19v3m6-3v3M2 9h3m-3 6h3m14-6h3m-3 6h3"/>',
  terminal:'<rect x="3" y="4" width="18" height="16" rx="3"/><path d="m7 9 3 3-3 3m6 0h4"/>',
  chart:'<path d="M4 3v17h17M8 15v-4m5 4V7m5 8v-6"/>',
  settings:'<circle cx="12" cy="12" r="3"/><path d="m9 3 1-1h4l1 3 3 1 3 2-1 3v3l1 2-2 3-3-1-2 3h-4l-1-3-3-1-3-2 1-3V9L3 7l3-2 3 1z"/>',
  refresh:'<path d="M20 8a8 8 0 0 0-14-3L3 8m0-5v5h5m-4 8a8 8 0 0 0 14 3l3-3m0 5v-5h-5"/>',
  power:'<path d="M12 2v10M6 5a9 9 0 1 0 12 0"/>',
  plus:'<path d="M12 5v14M5 12h14"/>',
  shield:'<path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6zM8 12l3 3 5-6"/>',
  search:'<circle cx="10" cy="10" r="6"/><path d="m15 15 6 6"/>',
  send:'<path d="m3 3 19 9-19 9 4-9-4-9zm4 9h15"/>',
  copy:'<rect x="8" y="8" width="12" height="13" rx="2"/><path d="M5 16H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v1"/>'
};
const icon = name => `<svg class="ic" viewBox="0 0 24 24" aria-hidden="true">${icons[name] || icons.grid}</svg>`;
$$('[data-icon]').forEach(el=>el.innerHTML=icon(el.dataset.icon));
const pages = {overview:['概览','你的模型网关，一目了然。'],accounts:['账号池','订阅账号、剩余额度与运行状态。'],keys:['API Keys','为每个客户端分配独立的访问密钥。'],models:['模型目录','优先使用 Kimi K3、DeepSeek Flash 及其他模型。'],playground:['调试台','选一个模型，让你的第一条请求跑起来。'],usage:['用量记录','每一次请求，都有迹可循。'],settings:['设置与日志','连接配置、服务管理与本地诊断。']};
let page='overview', state=null, models=[...goModels], allowedModels=null, accounts=[], keys=[], refreshBusy=false, modalCleanup=null, controller=null, usageOffset=0, usageTotal=0, logs={};
const number = n => Number(n || 0).toLocaleString('zh-CN');
const cost = n => '$' + Number(n || 0).toFixed(4);
const date = n => n ? new Date(n*1000).toLocaleString('zh-CN',{hour12:false}) : '—';
function errorMessage(e) { return e?.message || String(e); }
function toast(message, error=false) { const el=document.createElement('div');el.className='toast'+(error?' error':'');el.textContent=message;$('#toasts').append(el);setTimeout(()=>el.remove(),5500); }
async function api(path, method='GET', body) {
  let r;
  try { r=await fetch('/api/'+path,{method,headers:{'X-Mir-Workbench':'1',...(body!==undefined?{'Content-Type':'application/json'}:{})},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(45000)}); }
  catch(e){ throw new Error(e.name==='TimeoutError'?'请求超时，请检查网关日志':'无法连接工作台，请重新启动桌面程序'); }
  const data=await r.json().catch(()=>({error:`HTTP ${r.status}`}));
  if(!r.ok) throw new Error(typeof data.error==='string'?data.error:data.error?.message||`HTTP ${r.status}`);
  return data;
}
const admin = (path,method='GET',body) => api('admin/'+path,method,body);
async function busy(button, fn) { if(button?.disabled)return; if(button)button.disabled=true;try{return await fn();}catch(e){toast(errorMessage(e),true);}finally{if(button)button.disabled=false;} }
async function copy(value) { try{await navigator.clipboard.writeText(value);toast('已复制');}catch{toast('剪贴板不可用，请手动选中复制',true);} }
function empty(title,detail,large=false,action='') { return `<div class="empty ${large?'large glass':''}">${icon('cpu')}<h3>${esc(title)}</h3><p>${esc(detail)}</p>${action}</div>`; }
function openModal(title, html, setup) { closeModal();$('#modal-title').textContent=title;$('#modal-body').innerHTML=html;$('#modal').showModal();setup?.(); }
function closeModal() { modalCleanup?.();modalCleanup=null;$('#modal').close();$('#modal-body').replaceChildren(); }
$('#close-modal').onclick=closeModal;
$('#modal').addEventListener('cancel',e=>{e.preventDefault();closeModal();});
function modalError(e){let box=$('#modal-error');if(!box){box=document.createElement('div');box.id='modal-error';box.className='form-error';$('#modal-body').append(box);}box.textContent=errorMessage(e);}
async function confirmAction(title, message, action) {
  openModal(title,`<p>${esc(message)}</p><div class="dialog-footer"><button class="btn" id="confirm-cancel">取消</button><button class="btn danger" id="confirm-yes">确认</button></div>`,()=>{
    $('#confirm-cancel').onclick=closeModal;
    $('#confirm-yes').onclick=async()=>{const b=$('#confirm-yes');b.disabled=true;try{await action();closeModal();await refresh();}catch(e){modalError(e);}finally{b.disabled=false;}};
  });
}
async function navigate(next) {
  if(!pages[next])return;page=next;
  $$('.page').forEach(el=>el.hidden=el.id!=='page-'+page);
  $$('nav [data-page]').forEach(el=>{el.classList.toggle('active',el.dataset.page===page);el.setAttribute('aria-current',el.dataset.page===page?'page':'false');});
  $('#page-title').textContent=pages[page][0];$('#page-sub').textContent=pages[page][1];$('.content').scrollTop=0;
  try{await loadPage();}catch(e){toast(errorMessage(e),true);}
}
document.addEventListener('click',e=>{
  const n=e.target.closest('[data-page]');if(n)navigate(n.dataset.page);
  const add=e.target.closest('[data-action="add-account"]');if(add)addAccount();
  const cp=e.target.closest('[data-copy]');if(cp)copy(cp.dataset.copy);
  const folder=e.target.closest('[data-folder]');if(folder)busy(folder,()=>api('reveal','POST',{folder:folder.dataset.folder}));
});
async function refresh() {
  if(refreshBusy)return;refreshBusy=true;
  try{
    state=await api('state');renderState();if(state.running)await syncAccounts();await loadPage();
    $('#updated-at').textContent='更新于 '+new Date().toLocaleTimeString('zh-CN',{hour12:false});
  }catch(e){$('#notice').hidden=false;$('#notice').textContent=errorMessage(e);$('#updated-at').textContent='刷新失败 · 显示上次数据';}
  finally{refreshBusy=false;}
}
function renderState() {
  const running=state.running;
  $('#side-status').className='service-pill '+(running?'up':'down');
  $('#side-status span').textContent=running?'本地网关运行中':'本地网关未就绪';
  $('#service').innerHTML=icon('power')+(running?'重启网关':'启动网关');
  $('#account-count').textContent=running?state.health?.accounts??0:'—';
  $('#notice').hidden=running&&!state.error;
  $('#notice').textContent=state.error||'网关当前未运行。点击右上角「启动网关」，查看日志可了解启动失败原因。';
  const base=state.gateway;
  $('#endpoints').innerHTML=[['Base URL',base+'/v1'],['Kimi / DS',base+'/v1/chat/completions'],['Claude',base+'/v1/messages'],['GPT',base+'/v1/responses']].map(([label,value])=>`<div class="endpoint-row"><span>${label}</span><code>${esc(value)}</code><button class="text-btn" data-copy="${esc(value)}" title="复制 ${esc(label)}">${icon('copy')}</button></div>`).join('');
  $('#local-info').innerHTML=`<div>安装目录<code>${esc(state.repo)}</code></div><div>网关地址<code>${esc(base)}</code></div><div>工作台地址<code>${esc(state.workbench)}</code></div><div>官方客户端<code>${state.desktop_installed?'已检测到 Mirasim':'未检测到'}</code><button class="text-btn" id="open-desktop" ${state.desktop_installed?'':'disabled'}>打开官方 Mirasim ↗</button></div>`;
  $('#open-desktop').onclick=e=>busy(e.currentTarget,()=>api('desktop','POST',{}));
}
async function loadPage() {
  if(page==='models'){if(state?.running)await syncAccounts();renderModels();return;}
  if(page==='playground'){if(state?.running)await syncAccounts();renderModelOptions();return;}
  if(!state?.running){
    const target={overview:'#top-models',accounts:'#accounts-list',keys:'#keys-table',usage:'#usage-table'}[page];
    if(target)$(target).innerHTML=empty('等待网关启动','启动本地网关后加载真实数据。',page==='accounts');
    if(page==='overview')renderStats(null);
    if(page==='settings')await loadLogs();
    return;
  }
  if(page==='overview')await loadOverview();
  if(page==='accounts')await loadAccounts();
  if(page==='keys')await loadKeys();
  if(page==='usage')await loadUsage();
  if(page==='settings'){await loadSettings();await loadLogs();}
}
function renderStats(s) {
  const stats=[['可用账号',s?`${s.accounts.usable} / ${s.accounts.total}`:'—','就绪账号 / 账号总数','users'],['请求次数',s?number(s.usage_24h.requests):'—','最近 24 小时','chart'],['Token 用量',s?number(s.usage_24h.input_tokens+s.usage_24h.output_tokens):'—','最近 24 小时 · 输入 + 输出','cpu'],['估算费用',s?cost(s.usage_24h.cost):'—','按价格表计费 · 非订阅账单','key']];
  $('#stats').innerHTML=stats.map(([label,value,foot,ic])=>`<div class="stat glass"><div class="stat-label">${label}${icon(ic)}</div><div class="stat-value">${value}</div><div class="stat-foot">${foot}</div></div>`).join('');
}
async function loadOverview(){
  const s=await admin('summary');renderStats(s);
  $('#top-models').innerHTML=s.top_models.length?`<div class="table-wrap"><table><thead><tr><th>模型</th><th>请求</th><th>输入 Tokens</th><th>输出 Tokens</th><th>费用</th></tr></thead><tbody>${s.top_models.map(m=>`<tr><td>${esc(m.model)}</td><td>${number(m.requests)}</td><td>${number(m.input_tokens)}</td><td>${number(m.output_tokens)}</td><td>${cost(m.cost)}</td></tr>`).join('')}</tbody></table></div>`:empty('还没有模型调用记录','添加账号并发送一次请求后，这里会展示真实使用情况。');
}
async function syncAccounts(){
  const data=await admin('accounts');accounts=data.items||[];allowedModels=allowedModelList(accounts,data.allowed_models);
  if(allowedModels){models=[...allowedModels];$('#models-source').textContent='Go 套餐 · 仅 Kimi K3 / DeepSeek Flash / GLM 5.3 Flash；不是仓库的通用模型清单。';}
  else {models=[...builtinModels];$('#models-source').textContent=accounts.length?'套餐能力未完全识别；通用参考清单不代表订阅已授权。':'暂无账号；以下为通用参考清单，不代表订阅已授权。';}
  if(allowedModels&&!allowedModels.includes($('#test-model').value))$('#test-model').value=allowedModels[0]||'';
  if(allowedModels&&state){
    const base=state.gateway;
    $('#endpoints').innerHTML=[['Base URL',base+'/v1'],['Go 模型',base+'/v1/chat/completions']].map(([label,value])=>`<div class="endpoint-row"><span>${label}</span><code>${esc(value)}</code><button class="text-btn" data-copy="${esc(value)}" title="复制 ${esc(label)}">${icon('copy')}</button></div>`).join('');
  }
  renderModelOptions();
}
const quotaNumber=n=>n>0&&n<0.01?'<0.01':Number(n).toLocaleString('zh-CN',{maximumFractionDigits:2});
const quotaPercent=n=>n>0&&n<0.1?'<0.1%':n.toFixed(1)+'%';
function renderQuota(account){
  const windows=(account.quota_windows||[]).map(quotaWindow).sort((a,b)=>Number(b.weekly)-Number(a.weekly));
  if(!account.limits_fetched||!windows.length)return `<div class="quota-pending">${account.limits_fetched?'上游未返回配额窗口；当前额度未知。':'额度尚未读取，正在等待上游配额。'}<small>点击「刷新额度」获取，不以 0% 代替未知值。</small></div>`;
  return `<div class="quota-windows">${windows.map(q=>{
    const reset=q.reset?new Date(q.reset).toLocaleString('zh-CN',{hour12:false}):'上游未返回';
    const width=q.percent==null?0:Math.min(100,Math.max(0,q.percent));
    return `<div class="quota-window ${q.weekly?'weekly':''}"><div class="quota-title"><strong>${esc(q.name)}</strong><span class="${q.percent>=90?'quota-warning':''}">${q.unlimited?'不限额':q.percent==null?'未知':quotaPercent(q.percent)+' 已用'}</span></div><div class="quota-amount">${q.valid?`<b>${esc(quotaNumber(q.used))}</b><span> / ${q.unlimited?'不限额':esc(quotaNumber(q.budget))} 点数</span>`:'上游额度数值不完整'}</div><div class="meter" role="progressbar" aria-label="${esc(q.name)}已用比例" ${q.percent==null?'':`aria-valuenow="${width}" aria-valuemin="0" aria-valuemax="100"`}><span style="width:${width}%;${q.percent>=90?'background:var(--amber)':''}"></span></div><div class="quota-meta"><span>${q.unlimited?'无固定额度上限':q.valid?`剩余 ${esc(quotaNumber(q.remaining))} 点数`:'剩余额度未知'}</span><span>${q.scoped?'按模型子上限':'账号共享额度'}</span></div><div class="quota-reset">重置时间：${esc(reset)}${q.reset&&q.reset<Date.now()?'（已到期，待刷新）':''}</div></div>`;
  }).join('')}</div><div class="quota-updated ${account.quota_stale?'quota-warning':''}">${account.quota_stale?'上次快照 · ':''}${account.quota_fetched_at?'更新于 '+esc(date(account.quota_fetched_at)):'更新时间未知'}</div>`;
}
async function loadAccounts(){
  await syncAccounts();
  if(!accounts.length){$('#accounts-list').innerHTML=empty('添加你的第一个 Mirasim 账号','支持 GitHub / Google 授权、邮箱验证码和 Refresh Token。',true,'<button class="btn primary" data-action="add-account">添加账号</button>');return;}
  $('#accounts-list').innerHTML=accounts.map(a=>{
    const status=!a.enabled?['已停用','']:a.suspended?['已挂起','bad']:a.cooldown?['冷却中','warn']:a.used_ratio>=1?['额度耗尽','warn']:['可用','good'];
    return `<article class="panel glass"><div class="account-head"><div class="avatar">${esc((a.name||a.email||'M')[0].toUpperCase())}</div><div><h3>${esc(a.name||a.email)}</h3><p>${esc(a.email)}</p></div><span class="badge ${status[1]}">${status[0]}</span></div>${renderQuota(a)}<div class="account-details"><span>${esc(a.provider)}</span><span>套餐：${esc((a.plan||'未获取').toUpperCase())}</span><span>并发：${number(a.inflight)}</span>${a.cooldown?`<span>冷却 ${number(a.cooldown_seconds)} 秒</span>`:''}</div>${a.allowed_models?.length?`<div class="account-models">套餐模型：${a.allowed_models.map(esc).join(' · ')}</div>`:''}${a.quota_error?`<div class="account-error">额度刷新失败：${esc(a.quota_error)}</div>`:''}${a.last_error?`<div class="account-error">${esc(a.last_error)}</div>`:''}<div class="actions"><button class="btn" data-acc-refresh="${esc(a.id)}">刷新额度</button><button class="btn" data-acc-toggle="${esc(a.id)}">${a.enabled?'停用':'启用'}</button><button class="btn danger" data-acc-delete="${esc(a.id)}">移除</button></div></article>`;
  }).join('');
  $$('[data-acc-refresh]').forEach(b=>b.onclick=()=>busy(b,async()=>{await admin(`accounts/${encodeURIComponent(b.dataset.accRefresh)}/refresh`,'POST',{});await loadAccounts();toast('额度已刷新');}));
  $$('[data-acc-toggle]').forEach(b=>b.onclick=()=>busy(b,async()=>{const a=accounts.find(x=>x.id===b.dataset.accToggle);await admin(`accounts/${encodeURIComponent(a.id)}`,'PATCH',{enabled:!a.enabled});await loadAccounts();}));
  $$('[data-acc-delete]').forEach(b=>b.onclick=()=>{const a=accounts.find(x=>x.id===b.dataset.accDelete);confirmAction('移除账号',`将 ${a.email} 从本地网关移除；不会注销你的 Mirasim 账号。`,()=>admin(`accounts/${encodeURIComponent(a.id)}`,'DELETE'));});
}
function addAccount(tab='oauth') {
  openModal('添加 Mirasim 账号',`<div class="dialog-tabs"><button data-add-tab="oauth" class="${tab==='oauth'?'active':''}">浏览器授权</button><button data-add-tab="email" class="${tab==='email'?'active':''}">邮箱验证码</button><button data-add-tab="token" class="${tab==='token'?'active':''}">粘贴 Token</button></div><div id="add-content"></div><div id="modal-error" class="form-error" role="alert"></div>`,()=>{
    $$('[data-add-tab]').forEach(b=>b.onclick=()=>addAccount(b.dataset.addTab));
    const success=async()=>{closeModal();toast('授权成功，账号已加入本地网关');await refresh();await navigate('accounts');};
    if(tab==='oauth'){
      $('#add-content').innerHTML='<p class="hint">在系统默认浏览器中授权，可使用已有 GitHub / Google 登录。官方客户端的登录态不会自动成为网关账号。</p><label class="field">登录方式<select id="oauth-provider"><option value="github">GitHub</option><option value="google">Google</option></select></label><label class="field">回调方式<select id="oauth-mode"><option value="auto">自动接收本机回调（推荐）</option><option value="manual">手动粘贴回调地址</option></select></label><div class="actions"><button class="btn primary" id="oauth-start">打开浏览器授权 ↗</button><button class="btn" id="oauth-reopen" hidden>重新打开授权页</button></div><div id="oauth-info" class="oauth-info" role="status"></div><div id="oauth-manual"><p class="hint">已经授权，但浏览器显示本地页面无法访问？先关闭弹窗查看账号池；若仍无账号，可直接粘贴刚才的完整回调链接恢复，无需再次授权。</p><label class="field">完整回调链接（手动恢复）<textarea id="callback-url" rows="3" placeholder="http://127.0.0.1:…/callback?access_token=…&refresh_token=…" autocomplete="off" spellcheck="false"></textarea></label><button class="btn" id="oauth-complete">从回调链接恢复账号</button></div>';
      let timer=null,active=true,flow=null,pollErrors=0,generation=0,importing=false;
      modalCleanup=()=>{active=false;generation++;clearTimeout(timer);};
      const enableRetry=()=>{const b=$('#oauth-start');b.disabled=false;b.textContent='重新发起授权 ↗';};
      async function poll(run){
        if(!active||!flow||run!==generation||importing)return;
        try{
          if(Date.now()/1000>=flow.expires_at)throw new Error('本次授权已过期，请重新发起或粘贴完整回调链接恢复。');
          const r=await admin('accounts/oauth/complete','POST',{flow_id:flow.flow_id});
          if(!active||run!==generation||importing)return;
          pollErrors=0;
          if(r.status==='done'){await success();return;}
          if(r.status==='error'){modalError(new Error(r.error));enableRetry();return;}
          $('#oauth-info').textContent='等待浏览器完成授权…（成功后自动进入账号池）';
          timer=setTimeout(()=>poll(run),1800);
        }catch(e){
          if(!active||run!==generation)return;
          pollErrors++;modalError(e);enableRetry();
          if(pollErrors<3&&Date.now()/1000<flow.expires_at)timer=setTimeout(()=>poll(run),2500);
        }
      }
      $('#oauth-start').onclick=async()=>{
        const b=$('#oauth-start');b.disabled=true;clearTimeout(timer);const run=++generation;pollErrors=0;$('#modal-error').textContent='';
        try{
          flow=await admin('accounts/oauth/start','POST',{provider:$('#oauth-provider').value,mode:$('#oauth-mode').value});if(!active||run!==generation)return;
          $('#oauth-info').textContent='等待浏览器完成授权…（10 分钟内有效）';
          $('#oauth-reopen').hidden=false;
          $('#oauth-provider').disabled=true;$('#oauth-mode').disabled=true;
          if(flow.mode==='auto')poll(run);
          await api('external','POST',{url:flow.auth_url});
          enableRetry();
        }catch(e){if(active){modalError(e);enableRetry();}}
      };
      $('#oauth-reopen').onclick=e=>busy(e.currentTarget,()=>api('external','POST',{url:flow.auth_url}));
      $('#oauth-complete').onclick=async()=>{const b=$('#oauth-complete');b.disabled=true;importing=true;clearTimeout(timer);try{const raw=$('#callback-url').value.trim();if(!raw)throw new Error('请先粘贴浏览器地址栏中的完整回调链接');await admin('accounts/oauth/complete','POST',{callback_url:raw,provider:flow?.provider||$('#oauth-provider').value});await success();}catch(e){if(active){modalError(e);b.disabled=false;importing=false;if(flow?.mode==='auto')poll(generation);}}};
    } else if(tab==='email') {
      $('#add-content').innerHTML='<label class="field">Mirasim 账号邮箱<input type="email" id="login-email" placeholder="you@example.com" autocomplete="email"></label><button class="btn" id="email-send">发送验证码</button><label class="field">邮箱验证码<input id="login-code" placeholder="输入收到的验证码" autocomplete="one-time-code"></label><div class="dialog-footer"><button class="btn primary" id="email-complete">验证并添加</button></div>';
      $('#email-send').onclick=async()=>{const b=$('#email-send');b.disabled=true;try{await admin('accounts/email/start','POST',{email:$('#login-email').value});toast('验证码已发送，请查收邮箱');}catch(e){modalError(e);}finally{b.disabled=false;}};
      $('#email-complete').onclick=async()=>{const b=$('#email-complete');b.disabled=true;try{await admin('accounts/email/complete','POST',{email:$('#login-email').value,code:$('#login-code').value});await success();}catch(e){modalError(e);b.disabled=false;}};
    } else {
      $('#add-content').innerHTML='<div class="dialog-note">请使用你自己的 Refresh Token（不是 Access Token）。凭据直接提交给本机网关，不会写入工作台日志。</div><label class="field">Refresh Token<textarea id="refresh-token" rows="5" placeholder="eyJ…" autocomplete="off" spellcheck="false"></textarea></label><div class="dialog-footer"><button class="btn primary" id="token-import">添加到账号池</button></div>';
      $('#token-import').onclick=async()=>{const b=$('#token-import');b.disabled=true;try{await admin('accounts/oauth/complete','POST',{token:$('#refresh-token').value.trim()});await success();}catch(e){modalError(e);b.disabled=false;}};
    }
  });
}
async function loadKeys(){
  keys=(await admin('keys')).items||[];
  if(!keys.length){$('#keys-table').innerHTML=empty('还没有 API Key','点击「创建密钥」，为客户端生成独立访问凭据。');return;}
  $('#keys-table').innerHTML=`<table><thead><tr><th>名称 / 密钥</th><th>状态</th><th>限制</th><th>模型范围</th><th>累计费用</th><th>操作</th></tr></thead><tbody>${keys.map(k=>`<tr><td>${esc(k.name)}<small><code>${esc(k.key_display)}</code></small></td><td><span class="badge ${k.enabled?'good':''}">${k.enabled?'启用':'停用'}</span><small>${k.expires_at?'到期 '+esc(date(k.expires_at)):'永不过期'}</small></td><td>并发 ${number(k.concurrency||5)}<small>RPM ${k.rate_limit_rpm||'不限'}</small></td><td>${k.model_allowlist?.length?esc(k.model_allowlist.join(', ')):'全部模型'}</td><td>${cost(k.total_cost)}</td><td><div class="actions"><button class="text-btn" data-key-edit="${esc(k.id)}">编辑</button><button class="text-btn" data-key-toggle="${esc(k.id)}">${k.enabled?'停用':'启用'}</button><button class="text-btn" data-key-delete="${esc(k.id)}">删除</button></div></td></tr>`).join('')}</tbody></table>`;
  $$('[data-key-edit]').forEach(b=>b.onclick=()=>keyDialog(keys.find(k=>k.id===b.dataset.keyEdit)));
  $$('[data-key-toggle]').forEach(b=>b.onclick=()=>busy(b,async()=>{const k=keys.find(k=>k.id===b.dataset.keyToggle);await admin(`keys/${encodeURIComponent(k.id)}`,'PATCH',{enabled:!k.enabled});await loadKeys();}));
  $$('[data-key-delete]').forEach(b=>b.onclick=()=>confirmAction('删除 API Key','此密钥将立即失效，关联客户端需要更换密钥。',()=>admin(`keys/${encodeURIComponent(b.dataset.keyDelete)}`,'DELETE')));
}
function keyDialog(existing=null){
  const k=existing||{name:'本地客户端',concurrency:5,rate_limit_rpm:0,model_allowlist:[]};
  openModal(existing?'编辑密钥限制':'创建 API Key',`<label class="field">名称<input id="key-name" value="${esc(k.name)}" ${existing?'disabled':''}></label><div class="grid-fields"><label class="field">并发上限<input id="key-concurrency" type="number" min="0" max="1000" value="${k.concurrency}"></label><label class="field">每分钟请求（0 = 不限）<input id="key-rpm" type="number" min="0" max="100000" value="${k.rate_limit_rpm}"></label></div><label class="field">模型白名单（留空允许所有模型）<textarea id="key-models" rows="2" placeholder="kimi-k3, deepseek-flash">${esc(k.model_allowlist?.join(', ')||'')}</textarea><small>多个模型用逗号、空格或换行分隔。</small></label><label class="field">过期时间（留空永不过期）<input id="key-expiry" type="datetime-local"></label><div class="dialog-footer"><button class="btn primary" id="key-submit">${existing?'保存更改':'创建密钥'}</button></div><div id="modal-error" class="form-error"></div>`,()=>{
    if(k.expires_at){const d=new Date(k.expires_at*1000);$('#key-expiry').value=new Date(d-d.getTimezoneOffset()*60000).toISOString().slice(0,16);}
    $('#key-submit').onclick=async()=>{
      const b=$('#key-submit');b.disabled=true;
      try{
        const concurrency=Number($('#key-concurrency').value),rpm=Number($('#key-rpm').value);
        if(!Number.isInteger(concurrency)||concurrency<0||concurrency>1000||!Number.isInteger(rpm)||rpm<0||rpm>100000)throw new Error('请检查并发和 RPM 的取值范围');
        const expiry=$('#key-expiry').value?Math.floor(new Date($('#key-expiry').value).getTime()/1000):0;
        if(expiry&&expiry<=Date.now()/1000)throw new Error('过期时间须在未来');
        const body={name:$('#key-name').value,concurrency,rate_limit_rpm:rpm,model_allowlist:$('#key-models').value.split(/[,，\s]+/).filter(Boolean),expires_at:expiry};
        const data=await admin(existing?`keys/${encodeURIComponent(k.id)}`:'keys',existing?'PATCH':'POST',body);
        if(existing){closeModal();toast('密钥限制已保存');await loadKeys();return;}
        openModal('密钥已创建 · 请立即保存',`<div class="dialog-note">明文只显示这一次，关闭后无法再次查看。</div><pre class="key-once" id="new-key"></pre><div class="actions"><button class="btn primary" id="copy-new-key">复制密钥</button><button class="btn" id="use-new-key">用于本窗口调试</button></div>`,()=>{
          $('#new-key').textContent=data.key;$('#copy-new-key').onclick=()=>copy(data.key);
          $('#use-new-key').onclick=()=>{$('#test-key').value=data.key;closeModal();navigate('playground');toast('已放入调试台；仅保留在此窗口内存');};
        });
        await loadKeys();
      }catch(e){modalError(e);b.disabled=false;}
    };
  });
}
function renderModels(){
  const term=$('#model-search').value.toLowerCase();
  const list=models.filter(m=>m.toLowerCase().includes(term));
  $('#models-list').innerHTML=list.length?list.map(m=>{const ep=endpointFor(m),preferred=m==='kimi-k3'||m==='deepseek-flash';return `<article class="model-card glass"><div class="model-card-head"><h3>${esc(m)}</h3>${preferred?'<span class="badge good">优先</span>':''}</div><p>默认协议 · ${esc(ep)}</p><div class="actions"><button class="btn" data-test-model="${esc(m)}">在调试台测试 →</button></div></article>`;}).join(''):empty('没有匹配的模型','试试其他关键词。',true);
  $$('[data-test-model]').forEach(b=>b.onclick=()=>{$('#test-model').value=b.dataset.testModel;$('#test-protocol').value='auto';updateProtocol();navigate('playground');});renderModelOptions();
}
function renderModelOptions(){$('#model-options').innerHTML=models.map(m=>`<option value="${esc(m)}"></option>`).join('');updateProtocol();}
function updateProtocol(){try{$('#protocol-hint').textContent='POST /v1/'+endpointFor($('#test-model').value,$('#test-protocol').value);}catch(e){$('#protocol-hint').textContent=errorMessage(e);}}
async function loadModels(){
  if(!$('#test-key').value.trim()){navigate('playground');$('#test-key').focus();toast('请先在调试台填写 API Key，再获取网关模型清单');return;}
  const r=await api('request','POST',{endpoint:'models',key:$('#test-key').value});
  const fetched=(r.data||[]).map(m=>m.id).filter(m=>typeof m==='string'&&(!allowedModels||allowedModels.includes(m)));
  if(!fetched.length)throw new Error('网关没有返回可用模型清单');
  models=fetched.sort((a,b)=>{const priority=x=>x==='kimi-k3'?0:x==='deepseek-flash'?1:2;return priority(a)-priority(b)||a.localeCompare(b);});
  $('#models-source').textContent=allowedModels?'来自网关 /v1/models · 已按 Go 套餐过滤，仅显示三个模型。':'来自网关 /v1/models（无账号或上游不可用时，网关会返回内置清单）。';renderModels();toast('模型清单已更新');
}
async function sendTest(){
  if(controller)return;
  if(allowedModels&&!allowedModels.includes($('#test-model').value.trim().replace(/^mirasim\//,''))){toast('Go 套餐仅支持 kimi-k3、deepseek-flash、glm-5.3-flash',true);return;}
  let endpoint,payload;
  try{endpoint=endpointFor($('#test-model').value,$('#test-protocol').value);payload=makePayload($('#test-model').value,$('#prompt').value,Number($('#test-max').value),endpoint);if(!$('#test-key').value.trim())throw new Error('请填写 API Key');}catch(e){toast(errorMessage(e),true);return;}
  controller=new AbortController();const started=performance.now();
  $('#send-test').disabled=true;$('#cancel-test').hidden=false;$('#answer-placeholder').hidden=true;$('#answer').hidden=false;$('#answer').textContent='';$('#request-status').textContent='正在连接…';
  let received=false;
  try{
    const r=await fetch('/api/request',{method:'POST',headers:{'Content-Type':'application/json','X-Mir-Workbench':'1'},body:JSON.stringify({endpoint,key:$('#test-key').value,payload}),signal:controller.signal});
    if(!r.ok){const raw=await r.text();let data;try{data=JSON.parse(raw);}catch{}throw new Error(data?.error?.message||data?.error||`HTTP ${r.status}: ${raw.slice(0,1000)}`);}
    if((r.headers.get('content-type')||'').includes('text/event-stream')){
      $('#request-status').textContent='正在接收…';
      await consumeSSE(r.body,event=>{const text=eventText(event);if(text){received=true;$('#answer').textContent+=text;$('.answer-area').scrollTop=$('.answer-area').scrollHeight;}});
    }else{
      const data=await r.json();const text=data.choices?.[0]?.message?.content||data.content?.filter(c=>c.type==='text').map(c=>c.text).join('')||JSON.stringify(data,null,2);$('#answer').textContent=text;received=true;
    }
    if(!received)throw new Error('流已结束，但未返回文本内容；请检查用量记录中的上游结果');
    $('#request-status').textContent=`完成 · ${((performance.now()-started)/1000).toFixed(1)} s`;
  }catch(e){
    if(e.name==='AbortError'){$('#request-status').textContent='已停止';if(!received)$('#answer').textContent='请求已取消。';}
    else{$('#request-status').textContent='请求失败';$('#answer').textContent+='\n'+errorMessage(e);toast(errorMessage(e),true);}
  }finally{controller=null;$('#send-test').disabled=false;$('#cancel-test').hidden=true;}
}
async function loadUsage(){
  const query=new URLSearchParams({since:$('#usage-since').value,model:$('#usage-model').value.trim(),limit:'25',offset:String(usageOffset)});
  const data=await admin('usage/logs?'+query);usageTotal=data.total;
  $('#usage-count').textContent=`共 ${number(data.total)} 条 · 第 ${Math.floor(usageOffset/25)+1} 页`;
  $('#usage-prev').disabled=usageOffset===0;$('#usage-next').disabled=usageOffset+25>=usageTotal;
  $('#usage-table').innerHTML=data.items.length?`<table><thead><tr><th>时间 / 模型</th><th>接口</th><th>状态</th><th>输入 / 输出</th><th>缓存</th><th>费用</th><th>耗时</th></tr></thead><tbody>${data.items.map(l=>`<tr><td>${esc(l.model)}<small>${esc(date(l.created_at))}</small></td><td><code>${esc(l.endpoint)}</code></td><td><span class="badge ${l.status<400?'good':'bad'}">${l.status}</span>${l.err?`<small title="${esc(l.err)}">${esc(l.err.slice(0,90))}</small>`:''}</td><td>${number(l.input_tokens)} / ${number(l.output_tokens)}</td><td>${number(l.cached_tokens)}</td><td>${cost(l.cost)}</td><td>${number(l.duration_ms)} ms</td></tr>`).join('')}</tbody></table>`:empty('当前筛选下暂无请求','实际模型调用后，网关会记录 Tokens、耗时与错误信息。');
}
const settingLabels={upstream_proxy:['出站代理','例如 http://127.0.0.1:7890；留空直连'],claude_cloak_mode:['Claude 模式','仅影响 Claude；不影响 Kimi / DeepSeek'],capacity_retries:['容量不足重试次数','0–10 次'],capacity_backoff_ms:['重试退避时间','0–60000 毫秒'],model_prices:['模型单价表（JSON）','每百万 Tokens 美元单价；input / output / cached'],rate_multiplier:['计费倍率','非负数字，仅用于估算费用']};
async function loadSettings(){
  const data=await admin('settings');
  $('#settings-form').innerHTML=data.settings.map(s=>{const [label,hint]=settingLabels[s.key]||[s.key,''];const input=s.key==='model_prices'?`<textarea data-setting="${esc(s.key)}" rows="4" spellcheck="false">${esc(s.value)}</textarea>`:s.key==='claude_cloak_mode'?`<select data-setting="${s.key}"><option value="relaxed" ${s.value==='relaxed'?'selected':''}>relaxed</option><option value="strict" ${s.value==='strict'?'selected':''}>strict</option></select>`:`<input data-setting="${esc(s.key)}" value="${esc(s.value)}" spellcheck="false">`;return `<label class="field ${s.key==='model_prices'?'wide':''}">${label}${input}<small>${hint} · 来源：${{database:'数据库',env:'环境变量',default:'默认值'}[s.source]||esc(s.source)}</small></label>`;}).join('');
}
async function loadLogs(){logs=await api('logs');$('#logs').textContent=logs[$('#log-kind').value]||'暂无日志';}
$('#source-link').onclick=e=>busy(e.currentTarget,()=>api('external','POST',{url:'https://github.com/Essaim8/mirasim2api'}));
$('#refresh').onclick=e=>busy(e.currentTarget,refresh);
$('#refresh-accounts').onclick=e=>busy(e.currentTarget,loadAccounts);
$('#service').onclick=()=>state?.running?confirmAction('重启网关','正在进行的请求会被中断，确认重启？',()=>api('service/restart','POST',{})):busy($('#service'),async()=>{await api('service/start','POST',{});await refresh();});
$('#stop-service').onclick=()=>confirmAction('停止网关','本地客户端将暂时无法调用模型；账号和密钥仍然保留。',()=>api('service/stop','POST',{}));
$('#create-key').onclick=()=>keyDialog();
$('#model-search').oninput=renderModels;$('#load-models').onclick=e=>busy(e.currentTarget,loadModels);
$('#test-model').oninput=updateProtocol;$('#test-protocol').onchange=updateProtocol;
$('#send-test').onclick=sendTest;$('#cancel-test').onclick=()=>controller?.abort();
$('#prompt').onkeydown=e=>{if((e.ctrlKey||e.metaKey)&&e.key==='Enter'){e.preventDefault();sendTest();}};
$('#clear-test').onclick=()=>{if(controller){toast('请先停止生成');return;}$('#answer').textContent='';$('#answer').hidden=true;$('#answer-placeholder').hidden=false;$('#request-status').textContent='等待发送';};
$('#usage-search').onclick=e=>busy(e.currentTarget,async()=>{usageOffset=0;await loadUsage();});
$('#usage-since').onchange=()=>busy(null,async()=>{usageOffset=0;await loadUsage();});
$('#usage-prev').onclick=()=>busy(null,async()=>{usageOffset=Math.max(0,usageOffset-25);await loadUsage();});
$('#usage-next').onclick=()=>busy(null,async()=>{usageOffset+=25;await loadUsage();});
$('#save-settings').onclick=e=>busy(e.currentTarget,async()=>{if(!state?.running)throw new Error('请先启动网关');const body={};$$('[data-setting]').forEach(el=>body[el.dataset.setting]=el.value);await admin('settings','PUT',body);toast('设置已保存并即时生效');await loadSettings();});
$('#settings-form').onsubmit=e=>e.preventDefault();
$('#refresh-logs').onclick=e=>busy(e.currentTarget,loadLogs);$('#log-kind').onchange=()=>$('#logs').textContent=logs[$('#log-kind').value]||'暂无日志';
$('#test-model').value='kimi-k3';
renderStats(null);renderModelOptions();refresh();
setInterval(()=>{if(!document.hidden&&!$('#modal').open&&!controller&&['overview','accounts'].includes(page))refresh();},12000);
