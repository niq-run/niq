// The `@worker` addressee label inside a message bubble. It carries the same
// affordances as the sender badge on the left of a row: a click mentions the
// worker into the input, and right-click / long-press opens the worker menu
// (details / show only this worker).
//
// It does not reuse WorkerBadge: a right-aligned bubble's label sits at the
// bubble's right edge, where the badge's floating tip would hang off-screen, and
// the `@` glyph itself is rendered by the row (the header reads "sender @ target").
import { useState } from 'react'
import { useTheme, fontSizes } from '../../theme'
import { useI18n } from '../../i18n'
import WorkerBadgeMenu from '../../components/WorkerBadgeMenu'
import { useContextMenu, contextMenuElementStyle } from '../../hooks/useContextMenu'
import type { RowCtx } from './RowCtx'

export default function MentionTarget({ id, ctx }: { id: string; ctx: RowCtx }) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  const { humanId, isPartner, onMention, onOpenDetail, onAddFilter, onFocusWorker, displayName } = ctx
  // Only a mentionable partner answers a click with a mention; the human and
  // non-partner ids (e.g. a workspace target) are labels only.
  const mentionable = id !== humanId && isPartner(id) && !!onMention
  const hasMenu = !!(onOpenDetail || onAddFilter || onFocusWorker)
  const contextMenu = useContextMenu((pos) => { if (hasMenu) setMenu(pos) })

  return (
    <span
      onClick={(e) => { e.stopPropagation(); if (mentionable) onMention?.(id) }}
      onContextMenu={(e) => { e.stopPropagation(); contextMenu.onContextMenu(e) }}
      onTouchStart={contextMenu.onTouchStart}
      onTouchMove={contextMenu.onTouchMove}
      onTouchEnd={contextMenu.onTouchEnd}
      onTouchCancel={contextMenu.onTouchCancel}
      title={mentionable ? t('badge.mention.tip') : undefined}
      className={mentionable ? 'badge-mention' : undefined}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 1,
        fontSize: fontSizes.sm,
        color: colors.accent,
        cursor: mentionable ? 'pointer' : (hasMenu ? 'context-menu' : 'default'),
        whiteSpace: 'nowrap',
        ...contextMenuElementStyle,
      }}
    >
      {/* Just "@name" — a message header should read as text, not as a row of
          boxed tokens. */}
      <span style={{ color: colors.textDimmed }}>@</span>
      {displayName(id)}
      {menu && (
        <WorkerBadgeMenu
          x={menu.x}
          y={menu.y}
          workerId={id}
          onDetail={onOpenDetail ? () => onOpenDetail(id) : () => {}}
          onReadme={onOpenDetail ? () => onOpenDetail(id, 'readme') : undefined}
          onFocus={onFocusWorker ? () => onFocusWorker(id) : () => {}}
          onAddFilter={onAddFilter ? () => onAddFilter(id) : undefined}
          onClose={() => setMenu(null)}
        />
      )}
    </span>
  )
}
