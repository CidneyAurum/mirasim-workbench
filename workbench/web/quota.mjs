export const goModels = ['kimi-k3','deepseek-flash','glm-5.3-flash'];
export function quotaWindow(window, index=0) {
  const raw = typeof window.name === 'string' ? window.name : '';
  const weekly = /^(7d|1w|weekly|week|seven_day|weekly_all)(\b|$)/i.test(raw);
  const name = weekly ? '每周额度'+(/^(7d|1w|weekly|week|seven_day|weekly_all)$/i.test(raw)?'':` · ${raw}`) : ({'5h':'5 小时额度','24h':'每日额度','1d':'每日额度'}[raw]||raw||`配额窗口 ${index+1}`);
  const valid = typeof window.used==='number' && Number.isFinite(window.used) && window.used>=0 && typeof window.budget==='number' && Number.isFinite(window.budget) && window.budget>=0;
  const unlimited = valid && window.budget===0;
  const percent = valid && !unlimited ? window.used/window.budget*100 : null;
  let reset = null;
  if (window.reset_at!=null && window.reset_at!=='') {
    const rawTime=window.reset_at;
    const time=typeof rawTime==='number'?rawTime*(rawTime<1e12?1000:1):Date.parse(rawTime);
    if(Number.isFinite(time)&&time>0)reset=time;
  }
  return {name,rawName:raw,weekly,valid,unlimited,percent,used:valid?window.used:null,budget:valid?window.budget:null,remaining:valid&&!unlimited?Math.max(0,window.budget-window.used):null,reset,scoped:!!window.model_scoped};
}
export function allowedModelList(accounts, explicit) {
  if(Array.isArray(explicit))return explicit;
  const enabled=accounts.filter(a=>a.enabled);
  return enabled.length && enabled.every(a=>String(a.plan).toLowerCase()==='go') ? [...goModels] : null;
}
