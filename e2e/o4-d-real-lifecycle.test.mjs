import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))

test('D controller lifecycle is exact SIGKILL, ready/current, and adopted once', async (t) => {
  const runner = await readFile(join(here, 'o4-b2-runner.mjs'), 'utf8')
  const validator = await readFile(join(here, 'o4-recovery-residue-record.mjs'), 'utf8')
  for (const marker of [
    "value.termination.requestedSignal === 'SIGKILL'",
    "value.termination.observedSignal === 'SIGKILL'",
    "value.newService.readiness === 'ready'",
    'value.newService.currentGeneration === true',
    'this.controllerAdopted = true',
    'registeredExternalService(proof.newService)',
    'pending.pid !== value.oldService.pid && pending.pid !== value.newService.pid',
  ]) assert.match(runner, new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
  assert.match(runner, /await validate0500Executable\(controller\.executableFile, controller\.sha256, 'service controller'\)/)
  assert.match(runner, /await waitForProcessGroupDeath\(pending\.pid/)
  assert.match(runner, /proof\.restartReceipt\.rawSha256 === sha256\(receiptBytes\)/)
  assert.match(validator, /service-control does not prove exact SIGKILL and full process-group death/)
  t.diagnostic('synthetic static protocol test only; controller/product/facilities were not executed')
})

test('D ordinary start has loopback readiness and generation binding while controller is not registered', async () => {
  const source = await readFile(join(here, 'o4-b2-runner.mjs'), 'utf8')
  assert.match(source, /uniqueValue\('--active-generation'\) === generationId/)
  assert.match(source, /httpRequest\(\{ host: '127\.0\.0\.1'.*path: '\/api\/o4\/residue'/)
  assert.match(source, /currentGenerationSha256 === sha256\(generationId\)/)
  assert.match(source, /installed service loopback readiness timed out/)
  const controllerBranch = source.slice(source.indexOf('async processBoundary()'), source.indexOf('async start(spec)'))
  assert.doesNotMatch(controllerBranch, /serviceHistory\.push|registeredProcess/)
  assert.match(controllerBranch, /this\.pendingController =/)
})

test('D evidence is product-published before independent owner-only reads', async (t) => {
  const source = await readFile(join(here, 'o4-recovery-residue-real.spec.ts'), 'utf8')
  const capture = source.slice(source.indexOf('async function captureTerminalScenario'),
    source.indexOf('async function captureAndScanScreenshot'))
  assert.ok(capture.indexOf('await publishProductEvidenceBoundary') <
    capture.indexOf('const resource = await readOwner0400JSON'))
  assert.ok(capture.indexOf('await publishProductEvidenceBoundary') <
    capture.indexOf('const sourceA3 = await tracker.nextA3'))
  assert.match(source, /createHmac\('sha256', authority\.sessionSecret\)/)
  assert.match(source, /readOwnerFile\(path, label, 8 \* 1024 \* 1024, 0o600\)/)
  assert.match(source, /productObservationDigest\('D', sequence, run\)/)
  assert.doesNotMatch(source, /publicObservationDigest: sha256\(canonical\(run\)\)/)
  t.diagnostic('synthetic static causality test only; no product, Docker, network, or OCR action ran')
})
