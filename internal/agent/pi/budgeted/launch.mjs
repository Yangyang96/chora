import { createHash } from 'node:crypto';
export function launchArguments(raw,digest,readFile) {
  if (Buffer.byteLength(raw)>256*1024 || createHash('sha256').update(raw).digest('hex')!==digest) throw Error('permission_contract_unsupported');
  const p=JSON.parse(raw);
  if (!Array.isArray(p.tools)||!p.tools.length||p.tools.length>7||!Array.isArray(p.originalRequired)||!Array.isArray(p.installed)||!Array.isArray(p.readOnlyPaths)||JSON.stringify(p.originalRequired)!==JSON.stringify(p.installed)) throw Error('permission_contract_unsupported');
  const allowed=['read','bash','edit','write','grep','find','ls'];
  if (new Set(p.tools).size!==p.tools.length||p.tools.some(t=>!allowed.includes(t))) throw Error('permission_contract_unsupported');
  const args=['--mode','rpc','--no-session','--no-extensions','--no-skills','--no-prompt-templates','--no-themes','--no-context-files','--provider','deepseek','--model','deepseek-v4-pro','--extension','/opt/chora/resource_check_observer.mjs','--extension','/opt/chora/budgeted/extension.mjs','--tools',p.tools.join(',')];
  for (const e of p.originalRequired) {
    if (!/^\/opt\/chora\/permissions\/[a-zA-Z0-9_-]+\.mjs$/.test(e.path)||![e.sha256,e.closure,e.semantics].every(s=>/^[0-9a-f]{64}$/.test(s))||createHash('sha256').update(readFile(e.path)).digest('hex')!==e.sha256) throw Error('permission_contract_unsupported');
    args.push('--extension',e.path);
  }
  return args;
}
