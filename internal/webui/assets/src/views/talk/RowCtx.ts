// The shared context handed to every talk row component: colors/l10n, the
// look-and-feel metrics, the identity helpers, and the interaction callbacks.
// Memoized by TalkView so its reference is stable, which is what lets the
// memoized rows skip re-rendering when only volatile per-row data changed.
import type { CSSProperties } from 'react'
import type { Palette } from '../../theme'
import type { StringKey } from '../../i18n'
import type { EventPayload } from '../../types'

export interface RowCtx {
  dark: boolean
  colors: Palette
  t: (key: StringKey, vars?: Record<string, string | number>) => string
  isMobile: boolean
  compactMode: boolean
  thinkingExpanded: boolean
  bubbleMax: string
  tPad: string
  tFontSize: number
  itemSep: CSSProperties
  humanId: string
  displayName: (id?: string) => string
  isReason: (id: string) => boolean
  directionOf: (evt: EventPayload, alignRight?: boolean) => string
  expandedContent: Set<string>
  toggleExpanded: (key: string) => void
  // Tool/request cards are an accordion: at most one is open (openToolId), so
  // only one code body is rendered/highlighted at a time.
  openToolId: string | null
  toggleTool: (key: string) => void
  onMention?: (id: string) => void
  onOpenDetail?: (id: string) => void
  onAddFilter?: (id: string) => void
  onFocusWorker?: (id: string) => void
  onTraceClick: (traceId: string) => void
  onDecide?: (id: string, approved: boolean, note?: string) => void
  scrollToEvent: (evtId: string) => void
}
