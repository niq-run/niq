import { useState } from 'react'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { vscDarkPlus, oneLight } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'

interface CollapsibleCodeProps {
  code: string
  language?: string
  // Bodies above this many chars offer the expand/collapse toggle. Default 260.
  foldThreshold?: number
  // Height of the clipped preview when folded (px). Default 200.
  foldHeight?: number
  // When false the built-in soft-wrap toolbar (top-right row) is hidden — the
  // caller renders its own wrap toggle elsewhere. Default true.
  showWrapToggle?: boolean
  // Optional controlled soft-wrap state; when provided, overrides internal state.
  wrap?: boolean
  onWrapChange?: (v: boolean) => void
}

// A code block with the same affordances as the tool bodies in the Talk view:
// a soft-wrap toggle and — for long bodies — a clipped preview with an
// expand/collapse control. Self-contained so any detail panel can drop it in.
export default function CollapsibleCode({ code, language = 'json', foldThreshold = 260, foldHeight = 200, showWrapToggle = true, wrap: wrapProp, onWrapChange }: CollapsibleCodeProps) {
  const { dark, colors } = useTheme()
  const { t } = useI18n()
  const [wrapState, setWrapState] = useState(true)
  const wrap = wrapProp ?? wrapState
  const setWrap = onWrapChange ?? setWrapState
  const [expanded, setExpanded] = useState(false)

  const foldable = code.length > foldThreshold
  const folded = foldable && !expanded
  const hlStyle = dark ? vscDarkPlus : oneLight
  // react-syntax-highlighter puts its own white-space on the <code> element,
  // so the wrap toggle must be applied there too, not just on the <pre>.
  const codeWrapStyle: React.CSSProperties = { whiteSpace: wrap ? 'pre-wrap' : 'pre', wordBreak: wrap ? 'break-word' : 'normal' }

  return (
    <div style={{ minWidth: 0 }}>
      {showWrapToggle && (
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 4 }}>
          <span
            onClick={() => setWrap(!wrap)}
            title={t('talk.wrap.tooltip')}
            style={{ cursor: 'pointer', color: colors.accentDim, fontSize: fontSizes.sm, textDecoration: 'underline dotted', userSelect: 'none' }}
          >
            {t('talk.wrap.toggle')}
          </span>
        </div>
      )}
      {/* Body: clipped preview when folded, full height otherwise. */}
      <div style={{ position: 'relative', maxHeight: folded ? foldHeight : undefined, overflow: folded ? 'hidden' : 'visible' }}>
        <SyntaxHighlighter
          language={language}
          style={hlStyle}
          codeTagProps={{ style: codeWrapStyle }}
          PreTag="div"
          customStyle={{
            margin: 0,
            padding: 0,
            background: 'transparent',
            fontSize: fontSizes.base,
            lineHeight: 1.5,
            fontFamily: 'monospace',
            whiteSpace: wrap ? 'pre-wrap' : 'pre',
            wordBreak: wrap ? 'break-word' : 'normal',
          }}
        >
          {code}
        </SyntaxHighlighter>
        {foldable && (
          <div
            style={{
              position: 'absolute', left: 0, right: 0, bottom: 0,
              height: folded ? 56 : 'auto',
              background: folded
                ? (dark
                    ? 'linear-gradient(to top, rgba(0,0,0,0.65) 0%, rgba(0,0,0,0) 100%)'
                    : 'linear-gradient(to top, rgba(255,255,255,0.85) 0%, rgba(255,255,255,0) 100%)')
                : 'transparent',
              display: 'flex', alignItems: folded ? 'flex-end' : 'center', justifyContent: 'center',
              padding: folded ? '0 0 8px' : '8px 0 0',
            }}
          >
            <span
              onClick={() => setExpanded(v => !v)}
              title={folded ? t('talk.code.expand.tooltip') : t('talk.code.collapse.tooltip')}
              style={{
                cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4,
                padding: '2px 10px', color: folded ? colors.accent : colors.textDimmed, fontSize: fontSizes.sm,
                userSelect: 'none', background: folded ? colors.bgChip : 'transparent', whiteSpace: 'nowrap',
              }}
            >
              {folded ? t('talk.code.expand') : t('talk.code.collapse')}
            </span>
          </div>
        )}
      </div>
    </div>
  )
}