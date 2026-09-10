import {spawn} from 'node:child_process'
import {resolve} from 'node:path'

// Explicitly enable both browser fixtures and reject missing/skipped cases.
// Hosting is deterministic here; real Pi/GitHub qualification is separate.
const required = [
 'TestStoppedWholeDataRootBackupRestoresMultiRepositoryTaskAtSamePath',
 'TestResultClosureDoesNotInheritAnotherRunOrResultDelivery',
 'TestTaskBranchBrowserFullDeliveryPreservesOriginalsAndRepositoryIsolation',
 'TestResultClosureBrowserTwoRepositories',
 'TestResultClosureUnreviewedRetainsEvidenceAndRefusesOldActions',
 'TestResultClosureRefusesUncertainPartialApply',
 'TestResultClosureMixedCommitPreservesFilesAndRejectsStaleCommit',
 'TestResultClosurePartialLegacyApplyRetainsExactHistory',
 'TestResultClosureFailedChecksRemainUnverified',
 'TestPiInstallationSeparatesRestartReadinessAndDrift',
 'TestPiInstallationCommandsRejectUnknownFieldsAndMissingProtection',
 'TestPreparePiInstallationAllowsTrueAbsenceButRejectsClaimedMissingSelection',
]
const child = spawn('go', ['test','-json','./internal/localweb','-run',`^(${required.join('|')})$`,'-count=1','-timeout=15m'], {
 env:{...process.env,CHORA_TASK_DELIVERY_BROWSER:'1',CHORA_RESULT_CLOSURE_BROWSER:'1',CHORA_DELIVERY_TEST_WEB:resolve('web/dist')},stdio:['ignore','pipe','inherit'],
})
let pending = '', failed = false
const passed = new Set()
child.stdout.setEncoding('utf8')
child.stdout.on('data', text => {
 pending += text
 const lines = pending.split('\n'); pending=lines.pop()
 for(const line of lines) {
  let event
  try {event=JSON.parse(line)} catch {failed=true; console.error('Invalid Go test event');continue}
  if(event.Output) process.stdout.write(event.Output)
  if(event.Action==='skip'||event.Action==='fail') failed=true
  if(event.Action==='pass'&&required.includes(event.Test)) passed.add(event.Test)
 }
})
child.on('error', error=>{console.error(error.message);process.exitCode=1})
child.on('close', code=>{
 const missing=required.filter(name=>!passed.has(name))
 if(code!==0||failed||missing.length||pending.trim()){
  console.error('PUBLIC_JOURNEY_FAILED', {code,failed,missing});process.exitCode=1
 }else console.log(`PUBLIC_JOURNEY_PASS: ${required.length} required scenarios, no skips; deterministic hosting`)
})
