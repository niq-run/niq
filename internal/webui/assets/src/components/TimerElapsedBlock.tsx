import { useTheme, fontSizes } from '../theme'
import { formatTime } from './talk-utils'
import type { EventPayload } from '../types'

interface TimerElapsedBlockProps {
  evt: EventPayload
}

export default function TimerElapsedBlock({ evt }: TimerElapsedBlockProps) {
  const { colors } = useTheme()
  const duration = evt.payload?.duration
  const label = evt.payload?.label

  return (
    <div style={{ textAlign: 'center', marginBottom: 12, fontSize: fontSizes.sm, color: colors.textDimmed, fontStyle: 'italic' }}>
      <span style={{ color: colors.textDim, marginRight: 4 }}>⏱</span>
      {label
        ? `Timer "${label}" elapsed after ${duration}ms`
        : `Timer elapsed after ${duration}ms`}
      <span style={{ marginLeft: 8, fontSize: fontSizes.xs, color: colors.textDimmed }}>{formatTime(evt.timestamp)}</span>
    </div>
  )
}