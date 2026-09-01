import { useRef, useState } from 'react'
import { useTheme, fontSizes } from '../theme'

// payloadInlineLimit is the largest payload rendered inline. Past it the payload
// is rendered only on demand: a request.completed provider.list snapshot carries
// every provider with every model each of them reports, and in practice runs to
// hundreds of kilobytes (a five-provider config measured ~187 KB) — far too
// large to drop into the event stream, and very expensive to syntax-highlight.
const payloadInlineLimit = 2000

// payloadPlainLimit marks the "super large" tier. Above it the payload is not
// even syntax-highlighted when revealed: highlighting hundreds of kilobytes is
// the slow part, so the reveal degrades to a plain, scrollable <pre>. Copy and
// download are offered instead so the content is still usable without drawing
// it into the DOM.
const payloadPlainLimit = 50 * 1024

function formatSize(chars: number): string {
  if (chars < 1024) return chars + ' chars'
  return (chars / 1024).toFixed(1) + ' KB'
}

interface PayloadGateProps {
  // json is the already-serialised payload; only its size is inspected, so the
  // caller stays in charge of how normal payloads are rendered (children).
  json: string
  children: React.ReactNode
}

// PayloadGate defers rendering an oversized payload: past payloadInlineLimit it
// shows a size hint instead of the content, and renders it once the user asks.
// Super-large payloads additionally get one-click copy and download, kept
// available after the reveal, and their reveal degrades to plain text rather
// than the caller's (highlighted) render.
export default function PayloadGate({ json, children }: PayloadGateProps) {
  const { colors } = useTheme()
  const [show, setShow] = useState(json.length <= payloadInlineLimit)
  const [copied, setCopied] = useState(false)
  const copyTimer = useRef<number | undefined>(undefined)
  const huge = json.length > payloadPlainLimit

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(json)
    } catch {
      // Clipboard API unavailable (non-secure context); fall back to a hidden
      // textarea so the affordance still works.
      const ta = document.createElement('textarea')
      ta.value = json
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
    }
    setCopied(true)
    window.clearTimeout(copyTimer.current)
    copyTimer.current = window.setTimeout(() => setCopied(false), 1500)
  }

  const download = () => {
    const blob = new Blob([json], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'payload-' + Date.now() + '.json'
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  }

  const actionStyle: React.CSSProperties = {
    cursor: 'pointer',
    display: 'inline-block',
    padding: '3px 9px',
    borderRadius: 4,
    border: '1px dashed ' + colors.border,
    color: colors.textDim,
    fontSize: fontSizes.sm,
    userSelect: 'none',
  }

  // copy/download stay reachable both before and after the reveal.
  const actions = huge ? (
    <>
      <span onClick={copy} className="btn-hover" style={actionStyle} title="copy the raw payload to the clipboard">
        {copied ? 'copied' : 'copy'}
      </span>
      <span onClick={download} className="btn-hover" style={actionStyle} title="download the raw payload as JSON">
        download
      </span>
    </>
  ) : null

  const actionRow = (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>{actions}</div>
  )

  if (show) {
    if (huge) {
      return (
        <div>
          {actionRow}
          {/* Degraded reveal: a scrollable plain <pre>, never the highlighter. */}
          <pre
            style={{
              margin: '6px 0 0',
              padding: '6px 8px',
              background: colors.bgChip,
              borderRadius: 4,
              fontSize: fontSizes.sm,
              lineHeight: 1.4,
              maxHeight: 480,
              overflow: 'auto',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-word',
              color: colors.text,
            }}
          >
            {json}
          </pre>
        </div>
      )
    }
    return <>{children}</>
  }

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
      <span onClick={() => setShow(true)} className="btn-hover" style={actionStyle} title="render this payload">
        payload too large ({formatSize(json.length)}) — click to render{huge ? ' as plain text' : ''}
      </span>
      {actions}
    </div>
  )
}
