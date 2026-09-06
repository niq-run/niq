import { useEffect, useRef, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { vscDarkPlus, oneLight } from 'react-syntax-highlighter/dist/esm/styles/prism'

// JsonEditor is a syntax-highlighted JSON editor styled after the talk view's
// tool bodies: a highlighted <pre> from the same react-syntax-highlighter sits
// underneath a transparent-text textarea (highlighting under the caret), with
// the talk view's soft-wrap toggle. It grows with its content — the tab keeps
// it out of the way, so no folding is needed.
export default function JsonEditor({ value, onChange, dark, colors }: {
  value: string
  onChange: (v: string) => void
  dark: boolean
  colors: any
}) {
  const { t } = useI18n()
  const [wrap, setWrap] = useState(true)
  const taRef = useRef<HTMLTextAreaElement>(null)

  // Auto-height: the textarea grows with its content, so the editor's height
  // always equals the whole body. The highlight backdrop is inset-0 of the
  // same box, so the two layers never drift and no scroll syncing is needed.
  useEffect(() => {
    const ta = taRef.current
    if (!ta) return
    ta.style.height = 'auto'
    ta.style.height = ta.scrollHeight + 'px'
  }, [value, wrap])

  const hlStyle = dark ? vscDarkPlus : oneLight
  const layer: React.CSSProperties = {
    fontFamily: 'monospace',
    fontSize: fontSizes.base,
    lineHeight: 1.5,
    whiteSpace: wrap ? 'pre-wrap' : 'pre',
    wordBreak: wrap ? 'break-word' : 'normal',
    padding: '10px 12px',
    margin: 0,
    boxSizing: 'border-box',
  }
  return (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 4 }}>
        <span
          onClick={() => setWrap(!wrap)}
          title={t('talk.wrap.tooltip')}
          style={{ cursor: 'pointer', color: colors.accentDim, fontSize: fontSizes.sm, textDecoration: 'underline dotted', userSelect: 'none' }}
        >
          {t('talk.wrap.toggle')}
        </span>
      </div>
      <div style={{ position: 'relative', minHeight: 260, display: 'flex', flexDirection: 'column' }}>
        {/* Highlight layer: opaque background + border, painted under the caret */}
        <div
          style={{ position: 'absolute', top: 0, right: 0, bottom: 0, left: 0, overflow: 'hidden', background: colors.bgLight, border: '1px solid ' + colors.border, borderRadius: 4 }}
        >
          <SyntaxHighlighter
            language="json"
            style={hlStyle}
            PreTag="div"
            codeTagProps={{ style: { ...layer, overflow: 'visible' } }}
            customStyle={{ ...layer, background: 'transparent', overflow: 'visible', height: 'fit-content' }}
          >
            {value}
          </SyntaxHighlighter>
        </div>
        {/* Input layer: invisible text (the highlight shows through), real caret */}
        <textarea
          ref={taRef}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          spellCheck={false}
          style={{ ...layer, position: 'relative', width: '100%', background: 'transparent', color: 'transparent', caretColor: colors.text, border: '1px solid transparent', borderRadius: 4, outline: 'none', resize: 'none', overflow: 'hidden', display: 'block' }}
        />
      </div>
    </div>
  )
}
