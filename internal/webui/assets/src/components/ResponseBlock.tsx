import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useTheme, fontSizes } from '../theme'
import { getContentText, oneLine, splitSystemReminder } from './talk-utils'
import { makeMdComponents } from './MarkdownComponents'
import type { EventPayload } from '../types'

interface ResponseBlockProps {
  evt: EventPayload
  quotedText?: string | null
  quotedWorker?: string | null
  // When set, the quoted box is clickable and jumps to the original event.
  quotedEvtId?: string
  onQuoteClick?: (evtId: string) => void
}

export default function ResponseBlock({ evt, quotedText, quotedWorker, quotedEvtId, onQuoteClick }: ResponseBlockProps) {
  const { dark, colors } = useTheme()
  const text = getContentText(evt)
  const lineColor = dark ? 'rgba(128,128,128,0.3)' : 'rgba(128,128,128,0.25)'
  const quoted = quotedText ? splitSystemReminder(quotedText) : null

  if (!quotedText) {
    return (
      <div
        style={{
          marginBottom: 12,
          background: colors.bgLight,
          border: '1px solid ' + colors.border,
          padding: '12px 16px',
          fontSize: fontSizes.md,
          lineHeight: 1.6,
          color: colors.text,
        }}
      >
        <div className="md-content">
          <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{text}</Markdown>
        </div>
      </div>
    )
  }

	  return (
		<div style={{ marginBottom: 12 }}>
		  {/* Quote box — shows only the user content (strips the system-reminder),
			  collapsed to one line. Clicking jumps to the original message when it
			  is available in the current list. */}
		  <div
			onClick={quotedEvtId && onQuoteClick ? () => onQuoteClick(quotedEvtId!) : undefined}
			title={quotedEvtId && onQuoteClick ? 'jump to original message' : undefined}
			style={{
			  background: colors.bgLight,
			  border: '1px solid ' + colors.border,
			  padding: '8px 14px',
			  fontSize: fontSizes.sm,
			  lineHeight: 1.5,
			  color: colors.textDim,
			  fontStyle: 'italic',
			  cursor: quotedEvtId && onQuoteClick ? 'pointer' : undefined,
			  whiteSpace: 'nowrap',
			  overflow: 'hidden',
			  textOverflow: 'ellipsis',
			}}
		  >
			<div style={{ display: 'flex', gap: 6, alignItems: 'baseline' }}>
			  {quotedWorker && (
				<span style={{ color: colors.accent, fontWeight: 'bold', flexShrink: 0, fontStyle: 'normal' }}>
				  @{quotedWorker}
				</span>
			  )}
			  <span>{quoted && quoted.content ? oneLine(quoted.content, 200) : oneLine(quotedText ?? '', 200)}</span>
			</div>
		  </div>

      {/* Vertical connector line */}
      <div
        style={{
          marginLeft: 12,
          width: 2,
          height: 14,
          background: lineColor,
          borderRadius: 1,
        }}
      />

      {/* Response card */}
      <div
        style={{
          background: colors.bgLight,
          border: '1px solid ' + colors.border,
          padding: '12px 16px',
          fontSize: fontSizes.md,
          lineHeight: 1.6,
          color: colors.text,
        }}
      >
        <div className="md-content">
          <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{text}</Markdown>
        </div>
      </div>
    </div>
  )
}