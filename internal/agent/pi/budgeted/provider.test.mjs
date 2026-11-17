import test from 'node:test';
import assert from 'node:assert/strict';
import { install, marker, payload } from './provider.mjs';
function fixture(reply, change={}) {
  let config,calls=0,seen;
  install({registerProvider:(name,c)=>{assert.equal(name,'deepseek');config=c;}},()=>{
    let end; const done=new Promise(resolve=>{end=resolve;});
    return {events:[],push(e){this.events.push(e);},end(){end(this.events);},done};
  },async raw=>{calls++;seen=JSON.parse(raw);return typeof reply==='function'?reply():JSON.stringify(reply);});
  const model={provider:'deepseek',id:'deepseek-v4-pro',api:'openai-completions',baseUrl:'https://api.deepseek.com',...change};
  return {call:(context,options={})=>config.streamSimple(model,context,{apiKey:marker,maxTokens:8,...options}),get calls(){return calls;},get seen(){return seen;}};
}
const response=(message={role:'assistant',content:'Answer'})=>({model:'deepseek-v4-pro',choices:[{message,finish_reason:message.tool_calls?'tool_calls':'stop'}],usage:{prompt_tokens:1,completion_tokens:2,total_tokens:3}});
const context={messages:[{role:'user',content:'Question'}]};
test('text -> local tool -> text uses a separately admitted request each time',async()=>{
  const tool={id:'call-1',type:'function',function:{name:'read',arguments:'{"path":"README.md"}'}};
  const f=fixture(response({role:'assistant',content:null,tool_calls:[tool]}));
  const events=await f.call(context).done;
  assert.equal(events.at(-1).type,'done');assert.equal(events.at(-1).reason,'toolUse');assert.equal(f.calls,1);
  const continuation={messages:[...context.messages,events.at(-1).message,{role:'toolResult',toolCallId:'call-1',content:[{type:'text',text:'scoped result'}]}]};
  const g=fixture(response());await g.call(continuation).done;
  assert.equal(g.calls,1);assert.equal(g.seen.messages[2].role,'tool');
});
test('final onPayload replacement goes to admission; onResponse precedes decoding',async()=>{
  let observed=false;
  const f=fixture(()=>'{malformed');
  const events=await f.call(context,{onPayload:()=>({model:'foreign-final-payload'}),onResponse:()=>{observed=true;}}).done;
  assert.equal(f.seen.model,'foreign-final-payload');assert.equal(observed,true);assert.equal(events.at(-1).type,'error');assert.equal(f.calls,1);
});
test('callback failures, abort, identity switches and unsupported content never fallback',async()=>{
  for (const options of [{onPayload:()=>{throw Error('secret');}},{signal:AbortSignal.abort()}]) {
    const f=fixture(response());const e=await f.call(context,options).done;assert.equal(f.calls,0);assert.equal(e.at(-1).type,'error');assert.ok(!JSON.stringify(e).includes('secret'));
  }
  const f=fixture(response());const e=await f.call(context,{onResponse:()=>{throw Error('callback');}}).done;assert.equal(f.calls,1);assert.equal(e.at(-1).type,'error');
  const g=fixture(response(),{id:'other'});await g.call(context).done;assert.equal(g.calls,0);
  assert.throws(()=>payload({messages:[{role:'user',content:[{type:'image',data:'x'}]}]},{maxTokens:8}));
});
test('unknown response model and bad tool result error after exactly one dispatch',async()=>{
  for (const reply of [{...response(),model:'other'},response({role:'assistant',tool_calls:[{id:'c',type:'function',function:{name:'read',arguments:'[]'}}]})]) {
    const f=fixture(reply);const e=await f.call(context).done;assert.equal(f.calls,1);assert.equal(e.at(-1).type,'error');
  }
});
test('bound output cap supplies default and rejects larger options before dispatch',async()=>{
 let config,calls=0,body;
 install({registerProvider:(_,c)=>config=c},()=>{let end;const done=new Promise(resolve=>end=resolve);return {push(){},end(){end();},done};},async raw=>{calls++;body=JSON.parse(raw);return JSON.stringify(response());},8);
 const model={provider:'deepseek',id:'deepseek-v4-pro',api:'openai-completions',baseUrl:'https://api.deepseek.com'};
 await config.streamSimple(model,context,{apiKey:marker}).done;assert.equal(body.max_tokens,8);assert.equal(calls,1);
 await config.streamSimple(model,context,{apiKey:marker,maxTokens:9}).done;assert.equal(calls,1);
 assert.throws(()=>install({registerProvider(){}},()=>{},()=>{},0));
});
