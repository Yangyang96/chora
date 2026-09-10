import { useI18n } from '../i18n'
import type { RunView } from '../types'

type Patch = NonNullable<RunView['reviewablePatch']>

function fileStats(file: Patch['files'][number]) {
  return {
    additions: file.additions ?? file.lines.filter((line) => line.kind === 'added').length,
    deletions: file.deletions ?? file.lines.filter((line) => line.kind === 'removed').length,
  }
}

export function ChangedFiles({ patch, provenanceLabel }: { patch?: Patch; provenanceLabel?: string }) {
  const { t } = useI18n()
  return (
    <section className="workbench-section" aria-labelledby="changed-files-title">
      <div className="workbench-section-head">
        <div>
          <span className="eyebrow">{t(provenanceLabel ?? 'Workbench')}</span>
          <h2 id="changed-files-title">{t('Changed files')}</h2>
        </div>
      </div>
      <div className="workbench-section-body">
        {!patch || patch.files.length === 0 ? (
          <p className="stream-empty">{t('No changed files.')}</p>
        ) : (
          <ul className="changed-files">
            {patch.files.map((file) => {
              const stats = fileStats(file)
              return (
                <li key={file.path}>
                  <code>{file.path}</code>
                  {file.kind && <span>{t({ added: 'Added', modified: 'Modified', deleted: 'Deleted' }[file.kind])}</span>}
                  <span className="changed-files-stats">
                    <span className="stat-add">+{stats.additions}</span> <span className="stat-del">−{stats.deletions}</span>
                  </span>
                </li>
              )
            })}
          </ul>
        )}
      </div>
    </section>
  )
}
