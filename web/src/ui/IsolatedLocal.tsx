import { ActionButton } from './ActionButton'
import { useI18n } from '../i18n'
import type { IsolatedLocalView } from '../types'

export function IsolatedLocal({ value, onPrepare }: { value: IsolatedLocalView; onPrepare: () => Promise<void> | void }) {
  const { t } = useI18n()
  const title = value.state === 'ready' ? 'Isolated Local is ready'
    : value.state === 'preparing' ? 'Preparing Isolated Local…'
    : value.state === 'failed' ? 'Isolated Local preparation failed'
    : value.state === 'restart_required' ? 'Restart Chora to finish preparation'
    : 'Prepare Isolated Local'
  return <section className="pi-discovery-inline" aria-label={t('Isolated Local readiness')}>
    <p role="status"><strong>{t(title)}</strong></p>
    {value.reason && <p>{value.reason}</p>}
    <p>{t('Pinned runtime: Pi {piVersion} · Node.js {nodeVersion}', { piVersion: value.piVersion, nodeVersion: value.nodeVersion })}</p>
    <p>{t('Runs in an isolated environment with private copies of selected repositories. Only the DeepSeek API key is provided from your native Pi login.')}</p>
    <p>{t('Configure DeepSeek in Pi in your shell before running an isolated task.')}</p>
    {value.state !== 'failed' && <dl>
      <dt>{t('Network')}</dt><dd>{t(value.policy.network)}</dd>
      <dt>{t('Resources')}</dt><dd>{t(value.policy.resources)}</dd>
      <dt>{t('Files')}</dt><dd>{t(value.policy.files)}</dd>
      <dt>{t('Credentials')}</dt><dd>{t(value.policy.credentials)}</dd>
    </dl>}
    {(value.state === 'not_prepared' || value.state === 'failed') && value.preparationAvailable &&
      <ActionButton type="button" className="btn-secondary" disabledReason="" onClick={() => void onPrepare()}>{t('Prepare Isolated Local')}</ActionButton>}
  </section>
}
