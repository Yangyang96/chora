// Fixed custom-provider interface for public Pi 0.85.1. No fetch, credential
// discovery, built-in provider fallback, or retry exists in this module.
export const marker = 'chora-channel-no-upstream-authority';
export const provider = 'deepseek';
export const modelID = 'deepseek-v4-pro';
const maxBytes = 256 * 1024;
const fail = () => { throw new Error('restricted_provider_contract'); };
const exact = (o, keys) => o && typeof o === 'object' && !Array.isArray(o) && Object.keys(o).every(k => keys.includes(k));
const text = s => { if (typeof s !== 'string') fail(); return s; };
export function payload(context, options = {}) {
  if (!exact(context, ['systemPrompt','messages','tools']) || !Array.isArray(context.messages)) fail();
  const messages = [];
  if (context.systemPrompt) messages.push({role:'system',content:text(context.systemPrompt)});
  for (const m of context.messages) {
    if (m.role === 'toolResult') {
      if (m.isError || !Array.isArray(m.content) || m.content.some(c => c.type !== 'text')) fail();
      messages.push({role:'tool',tool_call_id:text(m.toolCallId),content:m.content.map(c => text(c.text)).join('\n')});
    } else if (m.role === 'user') {
      const content = typeof m.content === 'string' ? m.content : Array.isArray(m.content) && m.content.every(c => c.type === 'text') ? m.content.map(c => text(c.text)).join('\n') : fail();
      messages.push({role:'user',content});
    } else if (m.role === 'assistant') {
      if (m.provider !== provider || m.model !== modelID || !Array.isArray(m.content)) fail();
      const content = [], calls = [];
      for (const c of m.content) {
        if (c.type === 'text') content.push(text(c.text));
        else if (c.type === 'toolCall') calls.push({id:text(c.id),type:'function',function:{name:text(c.name),arguments:JSON.stringify(c.arguments)}});
        else fail();
      }
      const message = {role:'assistant',content:content.length ? content.join('\n') : null};
      if (calls.length) message.tool_calls = calls;
      messages.push(message);
    } else fail();
  }
  if (!Number.isInteger(options.maxTokens) || options.maxTokens < 1 || options.maxTokens > 2048) fail();
  const body = {model:modelID,messages,max_tokens:options.maxTokens,thinking:{type:'disabled'},stream:false};
  if (context.tools?.length) body.tools = context.tools.map(t => {
    if (!exact(t,['name','description','parameters'])) fail();
    return {type:'function',function:{name:text(t.name),description:text(t.description),parameters:t.parameters}};
  });
  return body;
}
export function install(pi, createStream, dispatch, outputCap = 2048) {
  if (!Number.isInteger(outputCap)||outputCap<1||outputCap>2048) fail();
  pi.registerProvider(provider, {
    api:'openai-completions',baseUrl:'https://api.deepseek.com',apiKey:marker,authHeader:false,
    models:[{id:modelID,name:modelID,reasoning:false,input:['text'],cost:{input:0,output:0,cacheRead:0,cacheWrite:0},contextWindow:100000,maxTokens:2048}],
    streamSimple(model, context, options = {}) {
      const stream = createStream();
      const message = {role:'assistant',api:'openai-completions',provider,model:modelID,content:[],usage:{input:0,output:0,cacheRead:0,cacheWrite:0,totalTokens:0,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}},stopReason:'error',timestamp:Date.now()};
      // Pi requires numeric usage/cost fields. Zero cost is a display placeholder,
      // never a billing proof or admission input; Go uses immutable reservations.
      void (async () => {
        try {
          if (model.provider !== provider || model.id !== modelID || model.api !== 'openai-completions' || model.baseUrl !== 'https://api.deepseek.com' || options.apiKey !== marker || options.headers && Object.keys(options.headers).length || options.env && Object.keys(options.env).length || options.reasoning && options.reasoning !== 'off') fail();
          options.signal?.throwIfAborted();
          const capped={...options,maxTokens:options.maxTokens??outputCap};
          if (capped.maxTokens>outputCap) fail();
          let body = payload(context,capped);
          if (options.onPayload) body = (await options.onPayload(body,model)) ?? body;
          options.signal?.throwIfAborted();
          const raw = JSON.stringify(body);
          if (Buffer.byteLength(raw) > maxBytes) fail();
          // The Go boundary admits the FINAL replacement payload independently.
          const reply = await dispatch(raw,options.signal);
          options.signal?.throwIfAborted();
          if (options.onResponse) await options.onResponse({status:200,headers:new Headers({'content-type':'application/json'})},model);
          const response = JSON.parse(reply);
          if (response.model !== modelID || response.choices?.length !== 1 || !response.usage) fail();
          const choice = response.choices[0], m = choice.message;
          if (!exact(m,['role','content','tool_calls']) || m.role !== 'assistant' || !['stop','tool_calls'].includes(choice.finish_reason)) fail();
          const u = response.usage;
          if (![u.prompt_tokens,u.completion_tokens,u.total_tokens].every(n => Number.isSafeInteger(n) && n >= 0) || u.total_tokens !== u.prompt_tokens + u.completion_tokens) fail();
          message.usage.input=u.prompt_tokens; message.usage.output=u.completion_tokens; message.usage.totalTokens=u.total_tokens;
          stream.push({type:'start',partial:message});
          if (m.content !== null && m.content !== undefined) {
            const value=text(m.content), index=message.content.length;
            message.content.push({type:'text',text:''}); stream.push({type:'text_start',contentIndex:index,partial:message});
            message.content[index].text=value; stream.push({type:'text_delta',contentIndex:index,delta:value,partial:message});
            stream.push({type:'text_end',contentIndex:index,content:value,partial:message});
          }
          for (const call of m.tool_calls ?? []) {
            if (!exact(call,['id','type','function']) || call.type !== 'function' || !exact(call.function,['name','arguments'])) fail();
            const index=message.content.length, args=JSON.parse(text(call.function.arguments));
            if (!args || typeof args !== 'object' || Array.isArray(args)) fail();
            const tool={type:'toolCall',id:text(call.id),name:text(call.function.name),arguments:args};
            message.content.push(tool); stream.push({type:'toolcall_start',contentIndex:index,partial:message});
            stream.push({type:'toolcall_delta',contentIndex:index,delta:call.function.arguments,partial:message});
            stream.push({type:'toolcall_end',contentIndex:index,toolCall:tool,partial:message});
          }
          if ((choice.finish_reason === 'tool_calls') !== Boolean(m.tool_calls?.length)) fail();
          message.stopReason=choice.finish_reason === 'tool_calls' ? 'toolUse' : 'stop';
          stream.push({type:'done',reason:message.stopReason,message}); stream.end();
        } catch {
          message.stopReason=options.signal?.aborted ? 'aborted' : 'error'; message.errorMessage='restricted_provider_refused_or_unknown';
          stream.push({type:'error',reason:message.stopReason,error:message}); stream.end();
        }
      })();
      return stream;
    },
  });
}
