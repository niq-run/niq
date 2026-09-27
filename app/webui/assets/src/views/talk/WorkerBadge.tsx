// The speaker badge shown on rows and as the trailing run marker. Lives apart
// from rows.tsx because both rows and TalkView/StreamingTail use it directly.
import { useState } from 'react'
import { useTheme, fontSizes } from '../../theme'
import { useI18n } from '../../i18n'
import WorkerBadgeMenu from '../../components/WorkerBadgeMenu'
import { useContextMenu, contextMenuElementStyle } from '../../hooks/useContextMenu'

export default function WorkerBadge({ id, show, humanId, isReason, isPartner, onMention, onOpenDetail, onAddFilter, onFocusWorker, displayName, small }: {
  id: string
  show: boolean
  humanId: string
  isReason: (id: string) => boolean
  // isPartner (talk partner: reason / niw / remote-niw) gates single-click
  // mention. Falls back to isReason when absent (older callers).
  isPartner?: (id: string) => boolean
  onMention?: (id: string) => void
  onOpenDetail?: (id: string) => void
  onAddFilter?: (id: string) => void
  onFocusWorker?: (id: string) => void
  displayName: (id?: string) => string
  // small renders a compact trailing marker (font ~sm) instead of the big
  // speaker badge — used for the bottom-right run footer.
  small?: boolean
}) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [hover, setHover] = useState(false)
  // Context menu position (right-click / long-press), or null.
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const hasMenu = !!(onOpenDetail || onAddFilter || onFocusWorker)
  const contextMenu = useContextMenu((pos) => { if (hasMenu) setMenu(pos) })
  if (!show) return null
  const isHuman = id === humanId
  const partner = isPartner ? isPartner(id) : isReason(id)
  const mentionable = !isHuman && partner && !!onMention
  return (
    <span
      onClick={(e) => { e.stopPropagation(); if (mentionable) onMention?.(id) }}
      onContextMenu={(e) => { e.stopPropagation(); contextMenu.onContextMenu(e) }}
      onTouchStart={contextMenu.onTouchStart}
      onTouchMove={contextMenu.onTouchMove}
      onTouchEnd={contextMenu.onTouchEnd}
      onTouchCancel={contextMenu.onTouchCancel}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      className={mentionable ? 'badge-mention' : undefined}
      style={{
        cursor: mentionable ? 'pointer' : (hasMenu ? 'context-menu' : 'default'),
        fontSize: small ? fontSizes.sm : fontSizes.xxl,
        color: colors.accent,
        fontWeight: 'bold',
        fontFamily: 'monospace',
        ...contextMenuElementStyle,
      }}
    >
      {mentionable && (
        <span style={{ display: 'inline-block', overflow: 'hidden', whiteSpace: 'nowrap', verticalAlign: 'bottom', maxWidth: hover ? '1ch' : 0, transition: 'max-width 0.18s' }}>@</span>
      )}
      {displayName(id)}
      {mentionable && <span className="badge-tip">{t('badge.mention.tip')}</span>}
      {menu && (
        <WorkerBadgeMenu
          x={menu.x}
          y={menu.y}
          workerId={id}
          onDetail={onOpenDetail ? () => onOpenDetail(id) : () => {}}
          onFocus={onFocusWorker ? () => onFocusWorker(id) : () => {}}
          onClose={() => setMenu(null)}
        />
      )}
    </span>
  )
}
