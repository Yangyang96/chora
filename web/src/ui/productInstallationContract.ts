import type { ProductInstallationDoctor } from '../types'

const PRODUCT_INSTALLATION_SCHEMA = 'chora.product-installation-status/v1'
const statuses = new Set(['ready', 'blocked', 'completed'])
const reasonCodes = new Set([
  '',
  'observation_unavailable',
  'identity_mismatch',
  'authority_required',
  'idempotency_conflict',
  'installation_busy',
  'capability_probe_failed',
  'generation_referenced',
  'asset_identity_conflict',
  'invalid_request',
  'operation_failed',
])
const safeIdentifier = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/
const sensitiveMarker = /(credential|password|token|secret|grant|digest|\bpath\b|\bref\b)/i

const exactKeys = (value: Record<string, unknown>, keys: string[]) => {
  const actual = Object.keys(value).sort()
  const expected = [...keys].sort()
  return actual.length === expected.length && actual.every((key, index) => key === expected[index])
}

const record = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value)

const safePublicValue = (value: unknown, allowEmpty = true): value is string => (
  typeof value === 'string' && ((allowEmpty && value === '') || (safeIdentifier.test(value) && !/^[a-f0-9]{64}$/i.test(value) && !sensitiveMarker.test(value)))
)

export function parseProductInstallationDoctor(value: unknown): ProductInstallationDoctor | null {
  if (!record(value) || !exactKeys(value, [
    'schemaVersion', 'status', 'reasonCode', 'engine', 'activeGenerationId', 'candidateGenerationId', 'actions', 'replayed', 'restartRequired',
  ])) return null
  if (value.schemaVersion !== PRODUCT_INSTALLATION_SCHEMA || typeof value.status !== 'string' || !statuses.has(value.status)) return null
  if (typeof value.reasonCode !== 'string' || !reasonCodes.has(value.reasonCode)) return null
  if (!record(value.engine) || !exactKeys(value.engine, ['ready', 'apiVersion', 'operatingSystem', 'architecture', 'contextName'])) return null
  if (typeof value.engine.ready !== 'boolean' || !safePublicValue(value.engine.apiVersion) || !safePublicValue(value.engine.operatingSystem) || !safePublicValue(value.engine.architecture) || !safePublicValue(value.engine.contextName)) return null
  if (value.engine.ready && (value.engine.apiVersion === '' || value.engine.operatingSystem === '' || value.engine.architecture === '' || value.engine.contextName === '')) return null
  if (!safePublicValue(value.activeGenerationId) || !safePublicValue(value.candidateGenerationId)) return null
  if (!record(value.actions) || !exactKeys(value.actions, ['setup', 'upgrade', 'gc', 'uninstall'])) return null
  const actions = value.actions
  if (!['setup', 'upgrade', 'gc', 'uninstall'].every((action) => typeof actions[action] === 'boolean')) return null
  if (typeof value.replayed !== 'boolean' || typeof value.restartRequired !== 'boolean') return null
  return value as ProductInstallationDoctor
}
