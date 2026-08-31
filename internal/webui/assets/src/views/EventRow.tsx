import { useTheme, fontSizes } from '../theme'
import { type EventPayload } from '../types'
import { getTypeColor, summaryText, formatTime } from '../components/talk-utils'

interface EventRowProps {
  evt: EventPayload;
  selected: boolean;
  onSelect: () => void;
  onOpenWorker: (id: string) => void;
  deliveries: Record<string, string[]>;
  workerTypes: Record<string, string>;
  isMobile: boolean;
}

export default function EventRow({
  evt,
  selected,
  onSelect,
  onOpenWorker,
  deliveries,
  workerTypes,
  isMobile,
}: EventRowProps) {
  const { colors } = useTheme()
  const time = formatTime(evt.timestamp)
  const typeColor = getTypeColor(evt.type, colors)
  const recipients = deliveries[evt.id] || evt.recipients
  const reception = recipients
    ? recipients.join(", ")
    : evt.target_worker_id
      ? evt.target_worker_id
      : ""
  const content = summaryText(evt)
  const workerType = workerTypes[evt.worker_id] || ""

  // Table cells: nowrap + ellipsis. maxWidth:0 lets flexible columns truncate.
  // Vertical padding matches the worker list for a looser, consistent feel;
  // mobile rows are taller so they are easy to tap.
  const cell: React.CSSProperties = {
    padding: isMobile ? "16px 6px" : "10px 6px",
    borderBottom: "1px solid " + colors.eventRowBorder,
    verticalAlign: "top",
    whiteSpace: "nowrap",
    overflow: "hidden",
    textOverflow: "ellipsis",
    maxWidth: 0,
  }

  // Event type as a rounded tag with a tinted background.
  const tag: React.CSSProperties = {
    display: "inline-block",
    padding: "0 6px",
    borderRadius: 4,
    fontSize: fontSizes.xs,
    lineHeight: "16px",
    color: typeColor,
    background: typeColor + "1f",
    border: "1px solid " + typeColor + "55",
    whiteSpace: "nowrap",
  }

  // Worker type: plain dim text next to the worker ID.
  const workerTypeChip: React.CSSProperties = {
    marginLeft: 6,
    color: colors.textDimmed,
    fontSize: fontSizes.xs,
    whiteSpace: "nowrap",
  }

  return (
    <tr
      onClick={onSelect}
      className={"event-row" + (selected ? " selected" : "")}
      style={{ cursor: "pointer", userSelect: "none" }}
    >
      <td style={{ ...cell, color: colors.eventRowTime, fontSize: fontSizes.md, whiteSpace: "nowrap" }}>{time}</td>
      <td style={{ ...cell, fontSize: fontSizes.md }} title={evt.type}>
        <span style={tag}>{evt.type}</span>
      </td>
      <td style={{ ...cell, fontSize: fontSizes.md }}>
        {/* Worker ID: dotted underline, click to filter this source's events */}
        <span
          onClick={(e) => { e.stopPropagation(); onOpenWorker(evt.worker_id) }}
          title={"view events from " + evt.worker_id}
          style={{ color: colors.text, textDecoration: "underline dotted", cursor: "pointer" }}
        >
          {evt.worker_id}
        </span>
        {workerType && <span style={workerTypeChip}>{workerType}</span>}
      </td>
      <td style={{ ...cell, color: colors.text, fontSize: fontSizes.md }} title={reception}>
        {reception || ""}
      </td>
      <td style={{ ...cell, color: colors.textDim, fontSize: fontSizes.md }} title={content}>{content}</td>
    </tr>
  )
}
