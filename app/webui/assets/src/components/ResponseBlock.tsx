import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { getContentText, oneLine, splitSystemReminder } from './talk-utils'
import { makeMdComponents } from './MarkdownComponents'
import type { EventPayload } from '../types'

interface ResponseBlockProps {
  evt: EventPayload
  quotedText?: string | null
  quotedWorker?: string | null
  // When set, the quoted box is clickable and jumps to the original event.
  quotedEvtId?: string
  onQuoteClick?: (evtId: string) => void
}

// copyText writes to the clipboard, falling back to a hidden textarea when the
// async Clipboard API is unavailable (e.g. a non-secure context).
async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }
}

// ResponseCard is the response body: the rendered markdown plus a small copy
// affordance at its bottom-left. Right-clicking (or long-pressing) the card
// opens a menu with the same copy action, so the whole response can be taken
// without hand-selecting the rendered text.
function ResponseCard({ text, dark, colors, t }: {
  text: string
  dark: boolean
  colors: any
  t: (k: any) => string
}) {
  const [copied, setCopied] = useState(false)
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const timer = useRef<number | undefined>(undefined)

  useEffect(() => () => window.clearTimeout(timer.current), [])

  const copy = async () => {
    await copyText(text)
    setCopied(true)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => setCopied(false), 1500)
  }

  return (
    <div
      onContextMenu={(e) => { e.preventDefault(); setMenu({ x: e.clientX, y: e.clientY }) }}
      style={{
        background: colors.bgLight,
        border: '1px solid ' + colors.border,
        padding: '12px 16px',
        fontSize: fontSizes.md,
        lineHeight: 1.6,
        color: colors.text,
      }}
    >
      <div className="md-content">
        <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{text}</Markdown>
      </div>
      {/* Bottom-left copy affordance. */}
      <div style={{ display: 'flex', justifyContent: 'flex-start', marginTop: 8 }}>
        <span
          onClick={copy}
          className="btn-hover"
          title={t('response.copy.tooltip')}
          style={{
            cursor: 'pointer', userSelect: 'none', fontSize: fontSizes.xs,
            color: copied ? colors.toolCompleted : colors.textDimmed,
          }}
        >
          {copied ? t('response.copied') : t('response.copy')}
        </span>
      </div>
      {menu && (
        <CopyMenu
          x={menu.x} y={menu.y} copied={copied}
          onCopy={copy} onClose={() => setMenu(null)} t={t} colors={colors}
        />
      )}
    </div>
  )
}

// CopyMenu is the right-click menu on a response card: one copy action. Rendered
// through a portal so it escapes the message's stacking context; closes on
// outside click / Escape, mirroring WorkerBadgeMenu.
function CopyMenu({ x, y, copied, onCopy, onClose, t, colors }: {
  x: number
  y: number
  copied: boolean
  onCopy: () => void
  onClose: () => void
  t: (k: any) => string
  colors: any
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose()
    }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [onClose])

  const w = typeof window !== 'undefined' ? window.innerWidth : x
  const h = typeof window !== 'undefined' ? window.innerHeight : y
  return createPortal(
    <div
      ref={ref}
      style={{
        position: 'fixed', left: Math.min(x, w - 160), top: Math.min(y, h - 60),
        zIndex: 300, background: colors.bgLight, border: '1px solid ' + colors.border,
        borderRadius: 4, boxShadow: '0 4px 12px rgba(0,0,0,0.18)', padding: '3px 0',
      }}
    >
      <div
        className="btn-hover"
        onClick={() => { onCopy(); onClose() }}
        style={{ padding: '5px 12px', cursor: 'pointer', fontSize: fontSizes.sm, color: colors.text, whiteSpace: 'nowrap', userSelect: 'none' }}
      >
        {copied ? t('response.copied') : t('response.copy')}
      </div>
    </div>,
    document.body,
  )
}

export default function ResponseBlock({ evt, quotedText, quotedWorker, quotedEvtId, onQuoteClick }: ResponseBlockProps) {
  const { dark, colors } = useTheme()
  const { t } = useI18n()
  const text = getContentText(evt)
  const lineColor = dark ? 'rgba(128,128,128,0.3)' : 'rgba(128,128,128,0.25)'
  const quoted = quotedText ? splitSystemReminder(quotedText) : null

  if (!quotedText) {
    return (
      <div style={{ marginBottom: 12 }}>
        <ResponseCard text={text} dark={dark} colors={colors} t={t} />
      </div>
    )
  }

	  return (
		<div style={{ marginBottom: 12 }}>
		  {/* Quote box — shows only the user content (strips the system-reminder),
			  collapsed to one line. Clicking jumps to the original message when it
			  is available in the current list. */}
		  <div
			onClick={quotedEvtId && onQuoteClick ? () => onQuoteClick(quotedEvtId!) : undefined}
			title={quotedEvtId && onQuoteClick ? t('response.jump.tooltip') : undefined}
			style={{
			  background: colors.bgLight,
			  border: '1px solid ' + colors.border,
			  padding: '8px 14px',
			  fontSize: fontSizes.sm,
			  lineHeight: 1.5,
			  color: colors.textDim,
			  fontStyle: 'italic',
			  cursor: quotedEvtId && onQuoteClick ? 'pointer' : undefined,
			  whiteSpace: 'nowrap',
			  overflow: 'hidden',
			  textOverflow: 'ellipsis',
			}}
		  >
			<div style={{ display: 'flex', gap: 6, alignItems: 'baseline' }}>
			  {quotedWorker && (
				<span style={{ color: colors.accent, fontWeight: 'bold', flexShrink: 0, fontStyle: 'normal' }}>
				  @{quotedWorker}
				</span>
			  )}
			  <span>{quoted && quoted.content ? oneLine(quoted.content, 200) : oneLine(quotedText ?? '', 200)}</span>
			</div>
		  </div>

      {/* Vertical connector line */}
      <div
        style={{
          marginLeft: 12,
          width: 2,
          height: 14,
          background: lineColor,
          borderRadius: 1,
        }}
      />

      {/* Response card */}
      <ResponseCard text={text} dark={dark} colors={colors} t={t} />
    </div>
  )
}
