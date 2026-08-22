import { useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useTheme, fontSizes } from '../theme'
import { getContentText, formatTime } from './talk-utils'
import { makeMdComponents } from './MarkdownComponents'
import type { EventPayload } from '../types'

interface ThinkingBlockProps {
  evt: EventPayload
  defaultExpanded?: boolean
  compact?: boolean
}

export default function ThinkingBlock({ evt, defaultExpanded = true, compact = false }: ThinkingBlockProps) {
  const { dark, colors } = useTheme()
  const [collapsed, setCollapsed] = useState(!defaultExpanded)
  const text = getContentText(evt)

  const tPad = compact ? '4px 8px' : '6px 12px'
  const tFontSize = compact ? fontSizes.xs : fontSizes.base
  const tBorder = '1px solid ' + colors.border

  return (
    <div
      style={{
        marginBottom: compact ? 8 : 12,
        borderTop: tBorder,
        borderRight: tBorder,
        borderBottom: tBorder,
        borderLeft: tBorder,
        padding: tPad,
        fontSize: tFontSize,
        lineHeight: 1.5,
        color: colors.textDim,
      }}
    >
      <div
        onClick={() => setCollapsed(!collapsed)}
        style={{ cursor: 'pointer', userSelect: 'none', display: 'flex', alignItems: 'center', gap: 8 }}
      >
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <span style={{ width: 8, height: 8, borderRadius: 4, background: colors.eventType.reason, flexShrink: 0, opacity: 0.5 }} />
          <span style={{ color: colors.textDim, fontSize: tFontSize }}>Thinking</span>
        </span>
        <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
        <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{text.length} chars</span>
        <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
      </div>
      {!collapsed && text && (
        <div className="md-content" style={{ marginTop: 8, padding: '8px 0', borderTop: '1px solid ' + colors.borderLight }}>
          <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{text}</Markdown>
        </div>
      )}
    </div>
  )
}
