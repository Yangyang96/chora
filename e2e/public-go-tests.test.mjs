import assert from 'node:assert/strict'
import test from 'node:test'
import { planPublicTests } from './public-go-tests.mjs'

const group = { package: 'example/history', reason: 'Unpublished frozen fixture; maintainer gates execute it.', applicability: 'maintainer_historical_fixture', tests: ['TestHistorical'] }
test('only declared historical tests are omitted, leaving new tests eligible', () => {
  const plan = planPublicTests([{ package: group.package, test: 'TestHistorical' }, { package: group.package, test: 'TestNewPublicContract' }], [group])
  assert.equal(plan.historical.length, 1)
  assert.equal(new RegExp(plan.skipPatterns.get(group.package)).test('TestNewPublicContract'), false)
  assert.equal(new RegExp(plan.skipPatterns.get(group.package)).test('TestHistoricalExtra'), false)
})
test('stale, duplicated, ambiguous and wrong-package declarations fail closed', () => {
  assert.throws(() => planPublicTests([], [group]), /exactly once/)
  assert.throws(() => planPublicTests([{ package: 'other', test: 'TestHistorical' }], [group]), /declared package/)
  const listed = [{ package: group.package, test: 'TestHistorical' }]
  assert.throws(() => planPublicTests(listed, [group, group]), /exactly once/)
  assert.throws(() => planPublicTests([...listed, ...listed], [group]), /exactly once/)
  const plan = planPublicTests([...listed, { package: 'other', test: 'TestHistorical' }], [group])
  assert.equal(plan.skipPatterns.has('other'), false)
})
