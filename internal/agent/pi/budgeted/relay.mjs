import { once } from 'node:events';
export const maxFrame = 300 * 1024;
const fail = () => { throw new Error('restricted_channel_invalid'); };
const exact = (o, keys) => o && typeof o === 'object' && !Array.isArray(o) && Object.keys(o).every(k => keys.includes(k));
export async function* lines(input) {
  let pending = Buffer.alloc(0);
  for await (const chunk of input) {
    const data = Buffer.concat([pending,chunk]);
    let start=0, end;
    while ((end=data.indexOf(10,start)) >= 0) {
      if (end-start > maxFrame) fail();
      yield data.subarray(start,end).toString('utf8'); start=end+1;
    }
    pending=data.subarray(start);
    if (pending.length>maxFrame) fail();
  }
  if (pending.length) fail(); // a truncated frame cannot be replayed
}
export async function frame(output, f) {
  const raw=JSON.stringify(f)+'\n'; if (Buffer.byteLength(raw)>maxFrame) fail();
  if (!output.write(raw)) await once(output,'drain');
}
// child is a fixed Pi Process with a SEPARATE provider fd, supplied by wrapper.
// This module never launches a command or imports Pi; tests use pipe fixtures.
export async function relay(parentInput,parentOutput,child,providerPipe) {
  let pending='';
  const close=()=>{ parentInput.destroy(); child.stdin.destroy(); child.stdout.destroy(); providerPipe.destroy(); child.kill(); };
  try {
    await Promise.all([
      (async()=>{ for await (const raw of lines(child.stdout)) {
        await frame(parentOutput,{type:'pi_rpc',payload:JSON.parse(raw)});
      } throw new Error('pi_eof'); })(),
      (async()=>{ for await (const raw of lines(providerPipe)) {
        const f=JSON.parse(raw);
        if (!exact(f,['type','id','payload']) || f.type!=='dispatch_request' || !/^[a-zA-Z0-9_-]{1,128}$/.test(f.id) || pending || Buffer.byteLength(JSON.stringify(f.payload))>256*1024) fail();
        pending=f.id; await frame(parentOutput,f);
      } throw new Error('provider_eof'); })(),
      (async()=>{ for await (const raw of lines(parentInput)) {
        const f=JSON.parse(raw);
        if (f.type==='dispatch_result') {
          if (!exact(f,['type','id','payload','error']) || !pending || f.id!==pending) fail();
          pending=''; await frame(providerPipe,f);
        } else if (f.type==='pi_rpc') {
          if (!exact(f,['type','payload']) || !['prompt','abort','get_state'].includes(f.payload?.type)) fail();
          await frame(child.stdin,f.payload);
        } else fail();
      } throw new Error('control_eof'); })(),
    ]);
  } finally { close(); }
}
