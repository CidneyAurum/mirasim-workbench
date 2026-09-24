// The model families and endpoints match upstream docs/PROTOCOL.md.
// deepseek-v4-flash / deepseek-v4-flash-vision-exp 是 deepseek-flash 的历史别名，
// 官方客户端把它们归并到 deepseek-flash，参考清单不再单独列出（请求仍可按别名发送）。
export const builtinModels = ['kimi-k3','deepseek-flash','glm-5.3-flash','claude-opus-5','claude-sonnet-5','claude-fable-5','claude-haiku-4-5','claude-opus-4-8','gpt-6-astra'];
export function endpointFor(model, selected='auto') {
  model = model.replace(/^mirasim\//,'');
  const required = model.startsWith('claude-') ? 'messages' : model.startsWith('gpt-') ? 'responses' : null;
  if (selected !== 'auto' && required && selected !== required) throw new Error(`${model} 只支持 /v1/${required}`);
  return selected === 'auto' ? required || 'chat/completions' : selected;
}
export function makePayload(model, prompt, maxTokens, endpoint) {
  if (!model.trim() || !prompt.trim()) throw new Error('请填写模型和测试消息');
  if (!Number.isInteger(maxTokens) || maxTokens < 1 || maxTokens > 32768) throw new Error('最大输出 Tokens 须为 1–32768 的整数');
  const base = {model:model.trim(),stream:true};
  if (endpoint === 'responses') return {...base,input:[{role:'user',content:prompt}],max_output_tokens:maxTokens};
  return {...base,messages:[{role:'user',content:prompt}],max_tokens:maxTokens};
}
export function eventText(event) {
  if (event.error || event.type === 'error' || event.type === 'response.failed') {
    const error = event.error || event.response?.error;
    throw new Error(typeof error === 'string' ? error : error?.message || '上游流式响应失败');
  }
  if (event.type === 'response.output_text.delta') return event.delta || '';
  if (event.type === 'content_block_delta') return event.delta?.text || '';
  return event.choices?.[0]?.delta?.content || '';
}
export async function consumeSSE(body, onEvent) {
  const reader = body.getReader(), decoder = new TextDecoder();
  let buffer = '', data = [];
  const line = value => {
    if (value === '') {
      if (data.length) {
        const raw = data.join('\n'); data = [];
        if (raw !== '[DONE]') onEvent(JSON.parse(raw));
      }
    } else if (value.startsWith('data:')) data.push(value.slice(5).replace(/^ /,''));
  };
  try {
    for (;;) {
      const {value, done} = await reader.read();
      buffer += done ? decoder.decode() : decoder.decode(value,{stream:true});
      let index;
      while ((index = buffer.indexOf('\n')) !== -1) { line(buffer.slice(0,index).replace(/\r$/,'')); buffer = buffer.slice(index+1); }
      if (done) { if (buffer) line(buffer.replace(/\r$/,'')); line(''); break; }
    }
  } finally { reader.releaseLock(); }
}
