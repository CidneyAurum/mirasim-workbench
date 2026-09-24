import test from 'node:test';
import assert from 'node:assert/strict';
import {builtinModels,endpointFor,makePayload,eventText,consumeSSE} from './protocol.mjs';

test('Kimi / DeepSeek are first and use Chat Completions',()=>{
  assert.deepEqual(builtinModels.slice(0,2),['kimi-k3','deepseek-flash']);
  for(const model of ['kimi-k3','deepseek-flash','glm-5.3-flash']) {
    assert.equal(endpointFor(model),'chat/completions');
    assert.equal(endpointFor(model,'messages'),'messages');
  }
});
test('legacy DeepSeek alias routes but is not advertised',()=>{
  assert.ok(!builtinModels.includes('deepseek-v4-flash'));
  assert.ok(!builtinModels.includes('deepseek-v4-flash-vision-exp'));
  for(const model of ['deepseek-v4-flash','deepseek-v4-flash-vision-exp','mirasim/deepseek-flash']) {
    assert.equal(endpointFor(model),'chat/completions');
  }
});
test('Claude and GPT route only to supported endpoints',()=>{
  assert.equal(endpointFor('mirasim/claude-sonnet-5'),'messages');
  assert.equal(endpointFor('gpt-6-astra'),'responses');
  assert.throws(()=>endpointFor('gpt-6-astra','chat/completions'));
  assert.throws(()=>endpointFor('claude-sonnet-5','responses'));
});
test('all request shapes stream, and third-party system is not modified',()=>{
  const p=makePayload('kimi-k3','你好',512,'chat/completions');
  assert.deepEqual(p,{model:'kimi-k3',stream:true,messages:[{role:'user',content:'你好'}],max_tokens:512});
  assert.equal(makePayload('gpt-6-astra','hi',64,'responses').max_output_tokens,64);
  assert.throws(()=>makePayload('kimi-k3','',64,'chat/completions'));
  assert.throws(()=>makePayload('kimi-k3','hi',1.5,'chat/completions'));
});
test('three response protocols and SSE error propagation',()=>{
  assert.equal(eventText({choices:[{delta:{content:'Kimi'}}]}),'Kimi');
  assert.equal(eventText({type:'content_block_delta',delta:{text:'Claude'}}),'Claude');
  assert.equal(eventText({type:'response.output_text.delta',delta:'GPT'}),'GPT');
  assert.throws(()=>eventText({type:'error',error:{message:'capacity'}}),/capacity/);
  assert.throws(()=>eventText({type:'response.failed',response:{error:{message:'failed'}}}),/failed/);
});
test('fragmented CRLF, split UTF8, comments, multiline and final unterminated event',async()=>{
  const raw=': ping\r\ndata: {"choices":[{"delta":{"content":"你好"}}]}\r\n\r\ndata: {"type":"response.output_text.delta",\r\ndata: "delta":"!"}\r\n\r\ndata: [DONE]\r\n\r\ndata: {"type":"content_block_delta","delta":{"text":"末尾"}}';
  const bytes=new TextEncoder().encode(raw);
  const stream=new ReadableStream({start(c){for(let i=0;i<bytes.length;i+=2)c.enqueue(bytes.slice(i,i+2));c.close();}});
  let text='';await consumeSSE(stream,e=>text+=eventText(e));assert.equal(text,'你好!末尾');
});
