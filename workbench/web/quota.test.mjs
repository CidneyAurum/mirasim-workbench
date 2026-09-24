import test from 'node:test';
import assert from 'node:assert/strict';
import {goModels,quotaWindow,allowedModelList} from './quota.mjs';

test('Go contains exactly the three subscribed model IDs',()=>{
  assert.deepEqual(goModels,['kimi-k3','deepseek-flash','glm-5.3-flash']);
  assert.deepEqual(allowedModelList([{enabled:true,plan:'go'}],null),goModels);
  assert.equal(allowedModelList([{enabled:true,plan:'pro'}],null),null);
  assert.equal(allowedModelList([],null),null);
});
test('weekly quota preserves raw amounts and reset time',()=>{
  const q=quotaWindow({name:'7d',used:150.5,budget:500,reset_at:'2026-10-01T12:00:00Z'});
  assert.equal(q.name,'每周额度');assert.equal(q.weekly,true);
  assert.equal(q.used,150.5);assert.equal(q.budget,500);assert.equal(q.percent,30.099999999999998);
  assert.equal(q.remaining,349.5);assert.equal(q.reset,Date.parse('2026-10-01T12:00:00Z'));
});
test('unknown is not 0%, unlimited is not exhausted',()=>{
  assert.equal(quotaWindow({name:'7d'}).percent,null);
  assert.equal(quotaWindow({name:'7d',used:null,budget:500}).valid,false);
  const unlimited=quotaWindow({name:'7d',used:150,budget:0,reset_at:null});
  assert.equal(unlimited.unlimited,true);assert.equal(unlimited.percent,null);assert.equal(unlimited.reset,null);
});
test('hourly and model-scoped windows stay separate',()=>{
  const q=quotaWindow({name:'5h',used:250,budget:200,model_scoped:true});
  assert.equal(q.name,'5 小时额度');assert.equal(q.weekly,false);assert.equal(q.percent,125);assert.equal(q.remaining,0);assert.equal(q.scoped,true);
});
