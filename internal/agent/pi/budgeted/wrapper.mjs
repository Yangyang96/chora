// Installed read-only in the NEW restricted image only. Importing relay/provider
// in offline tests never executes this entry point or a real Pi process.
import { spawn } from 'node:child_process';
import { relay } from './relay.mjs';
const args=process.argv.slice(2);
const expected=['--mode','rpc','--no-session','--no-extensions','--no-skills','--no-prompt-templates','--no-themes','--no-context-files','--provider','deepseek','--model','deepseek-v4-pro','--extension','/opt/chora/resource_check_observer.mjs','--extension','/opt/chora/budgeted/extension.mjs'];
if (JSON.stringify(args)!==JSON.stringify(expected)) process.exit(78);
const environment={PATH:'/usr/local/bin:/usr/bin:/bin',HOME:'/run/chora/pi',PI_CODING_AGENT_DIR:'/run/chora/pi',PI_SKIP_VERSION_CHECK:'1',PI_TELEMETRY:'0'};
// Only the existing observer's non-secret frozen metadata crosses the boundary.
for (const k of ['CHORA_CHECK_OBSERVER_CONFIG','CHORA_CHECK_OBSERVER_CONFIG_SHA256','CHORA_CHECK_OBSERVER_HELPER','CHORA_CHECK_OBSERVER_HELPER_SHA256','CHORA_CHECK_OBSERVER_RUNTIME_FINGERPRINT','CHORA_CHECK_OBSERVER_SHA256','CHORA_ATTEMPT_ID','CHORA_POLICY_DIGEST','CHORA_ATTEMPT_IMAGE_ID','CHORA_CAPABILITY_BUNDLE_ID','CHORA_CAPABILITY_BUNDLE_SHA256']) {
  if (process.env[k]) environment[k]=process.env[k];
}
const child=spawn('/usr/local/bin/node',['/opt/pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js',...args],{cwd:'/workspace/repository',env:environment,stdio:['pipe','pipe','pipe','pipe']});
child.stderr.resume(); // never mix logs with authority frames or expose raw errors
child.on('error',()=>{process.stdin.destroy();});
try { await relay(process.stdin,process.stdout,child,child.stdio[3]); }
catch { process.exitCode=78; }
