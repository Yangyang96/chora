import test from 'node:test';
import assert from 'node:assert/strict';
import { PassThrough, Duplex, Readable } from 'node:stream';
import { setImmediate } from 'node:timers/promises';
import { lines, maxFrame, relay } from './relay.mjs';
test('line framing rejects oversize and truncated input; chunks do not change records',async()=>{
  const got=[];for await (const line of lines(Readable.from([Buffer.from('{"a":'),Buffer.from('1}\n{}\n')]))){got.push(line);}assert.deepEqual(got,['{"a":1}','{}']);
  await assert.rejects(async()=>{for await(const ignored of lines(Readable.from([Buffer.alloc(maxFrame+1,120)]))){void ignored;}});
  await assert.rejects(async()=>{for await(const ignored of lines(Readable.from([Buffer.from('{}')]))){void ignored;}});
});
test('RPC and model pipes stay distinct; EOF closes Pi fixture',async()=>{
  const input=new PassThrough(), output=new PassThrough(), requests=[], replies=[], commands=[];
  output.on('data',b=>requests.push(JSON.parse(String(b))));
  const fromPi=new PassThrough(), toPi=new PassThrough();toPi.on('data',b=>replies.push(JSON.parse(String(b))));
  const provider=Duplex.from({readable:fromPi,writable:toPi});
  let killed=false;
  const child={stdin:new PassThrough(),stdout:new PassThrough(),kill(){killed=true;}};
  child.stdin.on('data',b=>commands.push(JSON.parse(String(b))));
  const running=relay(input,output,child,provider);const ended=assert.rejects(running);
  input.write('{"type":"pi_rpc","payload":{"type":"prompt","message":"scoped"}}\n');
  child.stdout.write('{"type":"agent_start"}\n');
  fromPi.write('{"type":"dispatch_request","id":"one","payload":{"model":"deepseek-v4-pro"}}\n');
  await setImmediate();
  assert.equal(commands[0].type,'prompt');assert.deepEqual(requests.map(f=>f.type).sort(),['dispatch_request','pi_rpc']);
  input.write('{"type":"dispatch_result","id":"one","error":"result_unreplayable_no_retransmission"}\n');await setImmediate();assert.equal(replies[0].id,'one');
  input.end();await ended;assert.equal(killed,true);
});
test('model switching and an unsolicited dispatch result terminate the channel',async()=>{
  for(const frame of [{type:'pi_rpc',payload:{type:'set_model',modelId:'other'}},{type:'dispatch_result',id:'foreign',payload:{}}]){
    const input=new PassThrough(),output=new PassThrough(),provider=new PassThrough();output.resume();
    let killed=false;const child={stdin:new PassThrough(),stdout:new PassThrough(),kill(){killed=true;}};
    const done=assert.rejects(relay(input,output,child,provider));input.write(JSON.stringify(frame)+'\n');await done;assert.equal(killed,true);
  }
});
