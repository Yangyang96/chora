import { useId, useRef, useState } from 'react'
import type { ButtonHTMLAttributes, CSSProperties } from 'react'
import { createPortal } from 'react-dom'
import { useI18n } from '../i18n'

type ActionButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & { disabledReason: string }

// Keep native disabled semantics, with a focusable surface for the explanation.
// The wrapper never forwards clicks or Enter/Space to the disabled action.
export function ActionButton({ disabled, disabledReason, id, title, 'aria-describedby': describedBy, ...props }: ActionButtonProps) {
  const { t } = useI18n()
  const generatedID = useId()
  const buttonID = id ?? `${generatedID}-button`
  const reasonID = `${generatedID}-reason`
  const anchor = useRef<HTMLSpanElement>(null)
  const [open, setOpen] = useState(false)
  const [position, setPosition] = useState<CSSProperties>({})
  const reason = t(disabledReason)
  function show() {
    const box = anchor.current?.getBoundingClientRect()
    if (!box) return
    const width = Math.min(340, window.innerWidth - 24)
    setPosition({ width, left: Math.max(12, Math.min(box.left, window.innerWidth - width - 12)),
      ...(box.bottom < window.innerHeight / 2 ? { top: box.bottom + 8 } : { bottom: window.innerHeight - box.top + 8 }) })
    setOpen(true)
  }
  return <span ref={anchor} className={disabled ? 'disabled-action' : 'action-contents'}
    role={disabled ? 'group' : undefined} tabIndex={disabled ? 0 : undefined}
    aria-labelledby={disabled ? buttonID : undefined} aria-describedby={disabled ? reasonID : undefined}
    title={disabled ? reason : undefined}
    onMouseEnter={() => { if (disabled) show() }}
    onMouseLeave={() => { if (document.activeElement !== anchor.current) setOpen(false) }}
    onFocus={() => { if (disabled) show() }} onBlur={() => setOpen(false)}
    onClick={(event) => { if (disabled) { event.preventDefault(); event.stopPropagation(); event.currentTarget.focus(); show() } }}
    onKeyDown={(event) => {
      if (!disabled) return
      if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); setOpen(false) }
      if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); event.stopPropagation(); show() }
    }}>
    <button {...props} id={buttonID} disabled={disabled} title={disabled ? undefined : title}
      aria-describedby={[describedBy, disabled ? reasonID : undefined].filter(Boolean).join(' ') || undefined} />
    {disabled && createPortal(<span id={reasonID} role="tooltip" className="disabled-action-tooltip"
      style={{ ...position, visibility: open ? 'visible' : 'hidden' }}>{reason}</span>, document.body)}
  </span>
}
