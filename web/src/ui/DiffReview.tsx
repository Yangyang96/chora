import { useEffect, useId, useMemo, useState } from 'react'
import { useI18n } from '../i18n'
import type { RunView } from '../types'

type Patch = Pick<NonNullable<RunView['reviewablePatch']>, 'files'> & { rawDownload?: string }
type PatchFile = Patch['files'][number]
type PatchLine = PatchFile['lines'][number]
type DiffMode = 'unified' | 'split'

function fileStats(file: PatchFile) {
  return {
    additions: file.additions ?? file.lines.filter((line) => line.kind === 'added').length,
    deletions: file.deletions ?? file.lines.filter((line) => line.kind === 'removed').length,
  }
}

function fileHunks(file: PatchFile) {
  if (file.hunks?.length) return file.hunks
  const oldLines = file.lines.filter((line) => line.oldLine !== undefined).map((line) => line.oldLine as number)
  const newLines = file.lines.filter((line) => line.newLine !== undefined).map((line) => line.newLine as number)
  const oldStart = oldLines[0] ?? 0
  const newStart = newLines[0] ?? 0
  return [{
    header: `@@ -${oldStart} +${newStart} @@`,
    oldStart,
    oldCount: oldLines.length,
    newStart,
    newCount: newLines.length,
    lines: file.lines,
  }]
}

type SplitRow = { left?: PatchLine; right?: PatchLine }

function splitRows(lines: PatchLine[]): SplitRow[] {
  const rows: SplitRow[] = []
  for (let index = 0; index < lines.length;) {
    if (lines[index].kind === 'context') {
      rows.push({ left: lines[index], right: lines[index] })
      index += 1
      continue
    }
    const removed: PatchLine[] = []
    const added: PatchLine[] = []
    while (index < lines.length && lines[index].kind !== 'context') {
      if (lines[index].kind === 'removed') removed.push(lines[index])
      else added.push(lines[index])
      index += 1
    }
    for (let offset = 0; offset < Math.max(removed.length, added.length); offset += 1) {
      rows.push({ left: removed[offset], right: added[offset] })
    }
  }
  return rows
}

function DiffCell({ line, side }: { line?: PatchLine; side: 'old' | 'new' }) {
  const number = side === 'old' ? line?.oldLine : line?.newLine
  const marker = line?.kind === 'added' ? '+' : line?.kind === 'removed' ? '-' : ' '
  return (
    <div className={`diff-cell ${line ? `diff-${line.kind}` : 'diff-empty'}`}>
      <span className="diff-number">{number ?? ''}</span>
      <span className="diff-marker">{line ? marker : ''}</span>
      <code>{line?.text ?? ''}</code>
    </div>
  )
}

export function DiffReview({ patch, provenanceLabel }: { patch: Patch; provenanceLabel: string }) {
  const { t } = useI18n()
  const titleId = useId()
  const [selectedPath, setSelectedPath] = useState(patch.files[0]?.path ?? '')
  const [mode, setMode] = useState<DiffMode>('unified')

  useEffect(() => {
    if (!patch.files.some((file) => file.path === selectedPath)) {
      setSelectedPath(patch.files[0]?.path ?? '')
    }
  }, [patch, selectedPath])

  const selected = patch.files.find((file) => file.path === selectedPath) ?? patch.files[0]
  const totals = useMemo(() => patch.files.reduce((sum, file) => {
    const stats = fileStats(file)
    return { additions: sum.additions + stats.additions, deletions: sum.deletions + stats.deletions }
  }, { additions: 0, deletions: 0 }), [patch])

  if (!selected) return null
  const stats = fileStats(selected)
  const hunks = fileHunks(selected)

  return (
    <section className="diff-review" aria-labelledby={titleId}>
      <div className="diff-review-head">
        <div>
          <span className="eyebrow">{t(provenanceLabel)}</span>
          <h2 id={titleId}>{t('Changes')}</h2>
          <p>{t(patch.files.length === 1 ? '{count} file' : '{count} files', { count: patch.files.length })} · <span className="stat-add">+{totals.additions}</span> <span className="stat-del">−{totals.deletions}</span></p>
        </div>
        <div className="diff-actions">
          <div className="diff-mode" role="group" aria-label={t('Diff view')}>
            <button type="button" className={mode === 'unified' ? 'on' : ''} aria-pressed={mode === 'unified'} onClick={() => setMode('unified')}>{t('Unified')}</button>
            <button type="button" className={mode === 'split' ? 'on' : ''} aria-pressed={mode === 'split'} onClick={() => setMode('split')}>{t('Split')}</button>
          </div>
          {patch.rawDownload && <a className="diff-download" href={patch.rawDownload}>{t('Download patch')}</a>}
        </div>
      </div>

      <div className="diff-workbench">
        <nav className="diff-files" aria-label={t('Changed files')}>
          <div className="diff-files-label">{t('Files')}</div>
          {patch.files.map((file) => {
            const itemStats = fileStats(file)
            const name = file.path.split('/').at(-1) ?? file.path
            const directory = file.path.slice(0, Math.max(0, file.path.length - name.length)).replace(/\/$/, '')
            return (
              <button type="button" key={file.path} className={file.path === selected.path ? 'selected' : ''} aria-pressed={file.path === selected.path} onClick={() => setSelectedPath(file.path)}>
                <span className="diff-file-copy"><strong>{name}</strong>{directory && <span>{directory}</span>}</span>
                <span className="diff-file-stats"><span className="stat-add">+{itemStats.additions}</span><span className="stat-del">−{itemStats.deletions}</span></span>
              </button>
            )
          })}
        </nav>

        <div className="diff-content">
          <div className="diff-file-head">
            <code>{selected.path}</code>
            <span><span className="stat-add">+{stats.additions}</span> <span className="stat-del">−{stats.deletions}</span></span>
          </div>
          {hunks.map((hunk, hunkIndex) => (
            <div className="diff-hunk" key={`${hunk.header}:${hunkIndex}`}>
              <div className="diff-hunk-head"><code>{hunk.header}</code><span>{t('Hunk {current} of {total}', { current: hunkIndex + 1, total: hunks.length })}</span></div>
              {mode === 'unified' ? (
                <div className="diff-lines" role="table" aria-label={`${selected.path} hunk ${hunkIndex + 1}`}>
                  {hunk.lines.map((line, index) => (
                    <div className={`diff-line diff-${line.kind}`} role="row" key={`${line.oldLine ?? 0}:${line.newLine ?? 0}:${index}`}>
                      <span className="diff-number">{line.oldLine ?? ''}</span>
                      <span className="diff-number">{line.newLine ?? ''}</span>
                      <span className="diff-marker">{line.kind === 'added' ? '+' : line.kind === 'removed' ? '-' : ' '}</span>
                      <code>{line.text}</code>
                    </div>
                  ))}
                </div>
              ) : (
                <div className="diff-split" role="table" aria-label={`${selected.path} split hunk ${hunkIndex + 1}`}>
                  <div className="diff-split-labels"><span>{t('Before')}</span><span>{t('After')}</span></div>
                  {splitRows(hunk.lines).map((row, index) => (
                    <div className="diff-split-row" role="row" key={`${row.left?.oldLine ?? 0}:${row.right?.newLine ?? 0}:${index}`}>
                      <DiffCell line={row.left} side="old" />
                      <DiffCell line={row.right} side="new" />
                    </div>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
