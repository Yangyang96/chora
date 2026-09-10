import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))

test('E closure shape exactly matches production final validator', async (t) => {
  const runner = await readFile(join(here, 'o4-b2-runner.mjs'), 'utf8')
  const final = await readFile(join(here, 'o4-final-evidence-record.mjs'), 'utf8')
  for (const key of ['registeredServiceCount', 'closedServiceCount', 'services',
    'processGroupDead', 'descendantsDead', 'runnerModuleSha256']) {
    assert.match(runner, new RegExp(key))
    assert.match(final, new RegExp(key))
  }
  assert.match(runner, /if \(services\.length === 0\)/)
  assert.match(runner, /registeredServiceCount: services\.length, closedServiceCount: services\.length/)
  assert.match(runner, /services, processGroupDead: true, descendantsDead: true/)
  assert.match(final, /value\.registeredServiceCount > 0/)
  assert.match(final, /value\.closedServiceCount === value\.registeredServiceCount/)
  t.diagnostic('synthetic static validator alignment only; no real E evidence executed')
})

test('E delivers closure, then seals durable replacement, then closes transport', async () => {
  const runner = await readFile(join(here, 'o4-b2-runner.mjs'), 'utf8')
  const closeStart = runner.indexOf('async closeAllServices(request)')
  const sealStart = runner.indexOf('async sealA3Ledger(request)')
  const proxyStart = runner.indexOf('function proxyView')
  const closeBody = runner.slice(closeStart, sealStart)
  const sealBody = runner.slice(sealStart, proxyStart)
  assert.doesNotMatch(closeBody, /closeAfterResponse = true/)
  assert.match(sealBody, /writeExclusive\(/)
  assert.match(sealBody,
    /await replaceOwnedPath\(\{[\s\S]*sourcePath: temporary,[\s\S]*targetPath: request\.sourceA3LedgerFile,[\s\S]*sourceIdentity: temporaryIdentity,[\s\S]*targetIdentity: beforeIdentity/)
  assert.doesNotMatch(sealBody, /await rename\(/)
  assert.doesNotMatch(sealBody, /await syncDirectory\(/)
  assert.match(sealBody, /this\.closeAfterResponse = true/)
  assert.match(runner, /socket\.end\([\s\S]*if \(this\.closeAfterResponse\) this\.closeControlPlane/)
  assert.match(runner,
    /sealedStable\.info\.dev !== beforeInfo\.dev \|\| sealedStable\.info\.ino !== beforeInfo\.ino/)
})

test('E requires and forwards the installed model observation authority', async (t) => {
  const final = await readFile(join(here, 'o4-final-evidence-record.mjs'), 'utf8')
  const real = await readFile(join(here, 'o4-final-evidence-real.spec.ts'), 'utf8')
  assert.match(final, /'modelObservationFile'/)
  assert.match(final, /modelObservationFile:\s*p\.modelObservationFile/)
  assert.match(real, /modelObservationFile:\s*env\.modelObservationFile/)
  assert.match(real,
    /modelObservationFile:\s*path\('CHORA_O4_FINAL_MODEL_OBSERVATION_FILE'\)/)
  t.diagnostic('static fail-closed path wiring only; no real E evidence executed')
})
