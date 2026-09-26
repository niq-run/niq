// Floating jump-to-bottom: shown while the conversation isn't pinned to the
// bottom. Clicking re-pins so live updates follow again.
import type { Palette } from '../../theme'

export default function ScrollToBottomButton({ onClick, title, colors }: {
  onClick: () => void
  title: string
  colors: Palette
}) {
  return (
    <button
      onClick={onClick}
      title={title}
      style={{
        position: 'absolute', bottom: 12, left: '50%', transform: 'translateX(-50%)',
        width: 36, height: 36, borderRadius: '50%', padding: 0,
        border: '1px solid ' + colors.border, background: colors.bg, color: colors.textDim,
        cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center',
        boxShadow: '0 2px 8px rgba(0,0,0,0.25)', zIndex: 5,
      }}
    >
      {/* Drawn arrow — glyph baselines sit off-center in the mono font. */}
      <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
        <path d="M7 2v9M3.2 7.6 7 11.4 10.8 7.6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    </button>
  )
}
