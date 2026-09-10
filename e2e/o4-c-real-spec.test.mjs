import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))

test('C real journey remains dedicated, real-only, serially manifest/session bound', async (t) => {
  const source = await readFile(join(here, 'o4-profile-reuse-real.spec.ts'), 'utf8')
  assert.match(source, /CHORA_O4_PROFILE_REUSE_REAL === '1'/)
  assert.match(source, /await assertTotalSessionBinding\('C'\)/)
  assert.match(source, /CHORA_O4_TOTAL_MANIFEST_FILE/)
  assert.match(source, /CHORA_O4_TOTAL_SESSION_AUTHORITY_FILE/)
  assert.match(source, /CHORA_O4_TOTAL_TUPLE_IDENTITY/)
  assert.match(source, /CHORA_O4_TOTAL_RUNNER_SHA256/)
  assert.doesNotMatch(source, /synthetic_non_acceptance|inProcessTestTransport/)
  t.diagnostic('synthetic static contract only; no real C journey or facility executed')
})

test('total driver binds C and its production validator closure before spawn', async () => {
  const source = await readFile(join(here, 'o4-total-real.mjs'), 'utf8')
  for (const path of [
    'e2e/o4-profile-reuse-real.spec.ts',
    'e2e/o4-profile-reuse-record.mjs',
    'e2e/o4-candidate-install-orchestrator.mjs',
    'e2e/o4-installed-doctor-record.mjs',
  ]) assert.match(source, new RegExp(path.replaceAll('.', '\\.')))
  assert.ok(source.indexOf('await verifyExecutionModuleClosure(input.candidateConfig)') <
    source.indexOf("const binding = input.manifest.phases[phase]"))
  assert.match(source, /actualBytes\.equals\(sourceBytes\)/)
  assert.match(source, /runtime module .* drifted from its manifest-bound source/)
})
