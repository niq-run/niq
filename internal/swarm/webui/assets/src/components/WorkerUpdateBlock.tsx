import { useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { formatTime } from './talk-utils'
import type { EventPayload } from '../types'

interface WorkerUpdateBlockProps {
  evt: EventPayload
  compact?: boolean
}

// Worker meta-operation card (compress/rotate/etc). Styled like ThinkingBlock:
// a boxed left-aligned block with a status dot, a compact header row and an
// expandable detail area carrying the rest of the operator payload.
export default function WorkerUpdateBlock({ evt, compact = false }: WorkerUpdateBlockProps) {
  const { colors } = useTheme()
  const [collapsed, setCollapsed] = useState(false)

  const p = (evt.payload || {}) as Record<string, unknown>
  const op = (p.op as string) || ''
  const done = !!p.done
  const err = (p.error as string) || ''

  const tPad = compact ? '4px 8px' : '6px 12px'
  const tFontSize = compact ? fontSizes.xs : fontSizes.base
  const tBorder = '1px solid ' + colors.border

  // Show the full payload so the detail always has meaningful content.
  const detail = Object.keys(p).length > 0 ? JSON.stringify(p, null, 2) : ''

  return (
    <div
      className={collapsed ? 'block-card' : undefined}
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
        style={{ cursor: 'pointer', userSelect: 'none', display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}
      >
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
          <span style={{ width: 8, height: 8, borderRadius: 4, background: colors.eventType.worker, flexShrink: 0, opacity: 0.5 }} />
          <span style={{ color: colors.textDim, fontSize: tFontSize }}>Worker Update</span>
        </span>
        {op && (
          <>
            <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
            <span style={{ color: colors.textDim, fontFamily: 'monospace', fontSize: tFontSize }}>{op}</span>
            <span style={{ color: done ? colors.toolCompleted : colors.textDim, fontSize: fontSizes.sm }}>
              {done ? 'done' : 'requested'}
            </span>
          </>
        )}
        {err && err !== '<nil>' && (
          <>
            <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
            <span style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{err}</span>
          </>
        )}
        <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
      </div>
      {!collapsed && (
        <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + colors.borderLight }}>
          <pre
            style={{
              margin: 0,
              padding: '6px 8px',
              background: colors.bgChip,
              borderRadius: 4,
              fontSize: fontSizes.sm,
              lineHeight: 1.4,
              overflowX: 'auto',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-word',
              color: colors.text,
            }}
          >
            {detail || '(no other fields)'}
          </pre>
        </div>
      )}
    </div>
  )
}