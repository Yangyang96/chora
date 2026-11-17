import fs from 'node:fs';
import { randomUUID } from 'node:crypto';
import { createRequire } from 'node:module';
import { pathToFileURL } from 'node:url';
const installed = createRequire('/opt/pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js');
const { createAssistantMessageEventStream } = await import(pathToFileURL(installed.resolve('@earendil-works/pi-ai')));
import { install } from './provider.mjs';
import { lines, frame } from './relay.mjs';

// fd3 is created by the sealed wrapper. No pathname, socket listener or key.
export default function extension(pi) {
  const input=fs.createReadStream(null,{fd:3,autoClose:false});
  const output=fs.createWriteStream(null,{fd:3,autoClose:false});
  let pending;
  void (async()=>{
    try { for await (const raw of lines(input)) {
      const f=JSON.parse(raw);
      if (!pending || f.type!=='dispatch_result' || f.id!==pending.id) throw new Error('channel_mismatch');
      const request=pending; pending=undefined;
      if (f.error) request.reject(new Error('dispatch_refused_or_unknown'));
      else request.resolve(JSON.stringify(f.payload));
    } } catch { /* common channel failure below; no raw payload in diagnostics */ }
    pending?.reject(new Error('channel_closed')); pending=undefined;
  })();
  install(pi,createAssistantMessageEventStream,(raw,signal)=>new Promise((resolve,reject)=>{
    if (pending || signal?.aborted) { reject(new Error('channel_busy_or_aborted')); return; }
    const id=randomUUID();
    const abort=()=>{ reject(new Error('dispatch_aborted')); }; // no resend/refund; retain pending until result
    const cleanup=()=>signal?.removeEventListener('abort',abort);
    pending={id,resolve:r=>{cleanup();resolve(r);},reject:e=>{cleanup();reject(e);}};
    signal?.addEventListener('abort',abort,{once:true});
    frame(output,{type:'dispatch_request',id,payload:JSON.parse(raw)}).catch(()=>{
      pending?.reject(new Error('channel_write_unknown')); pending=undefined;
    });
  }));
}
