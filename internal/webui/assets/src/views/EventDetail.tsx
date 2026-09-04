import { useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { type EventPayload } from '../types'
import { getTypeColor, formatTime } from '../components/talk-utils'
import { makeMdComponents } from '../components/MarkdownComponents'
import PayloadGate from '../components/PayloadGate'
import CollapsibleCode from '../components/CollapsibleCode'

interface EventDetailProps {
  evt: EventPayload;
  deliveries: Record<string, string[]>;
  onClose: () => void;
}

export default function EventDetail({ evt, deliveries, onClose }: EventDetailProps) {
  const { dark, colors } = useTheme()
  const { t } = useI18n()
  const time = formatTime(evt.timestamp)
  const typeColor = getTypeColor(evt.type, colors)
  const [wrap, setWrap] = useState(true)
  // Serialised once and handed to the size gate, which decides whether the
  // (possibly very large) payload is rendered at all.
  const payloadJSON = JSON.stringify(evt.payload, null, 2)
  // Payload is "present" when the event carries any payload field (a non-empty
  // object). No payload → show a grey "no payload" note and hide the wrap toggle.
  const hasPayload = !!evt.payload && typeof evt.payload === 'object' && Object.keys(evt.payload).length > 0
  const isContentEvent =
    evt.type === "reason.thinking" ||
    evt.type === "reason.response" ||
    evt.type === "worker.input"
  const contentText = isContentEvent
    ? evt.type === "worker.input"
      ? (evt.payload?.text as string) || ""
      : Array.isArray(evt.payload?.content)
        ? (evt.payload.content as any[]).filter(Boolean).join("\n")
        : ""
    : ""
  const recipients = deliveries[evt.id] || evt.recipients

  const inputBlockStyle = evt.type === "worker.input"
    ? { background: colors.accentBg, border: "1px solid " + colors.accentBorder, color: colors.text }
    : evt.type === "reason.response"
      ? { background: colors.bgLight, border: "1px solid " + colors.border, color: colors.text }
      : { background: colors.bgLight, border: "1px dotted " + colors.border, color: colors.textDim, fontStyle: 'italic' as const }

  const tag: React.CSSProperties = {
    display: "inline-block",
    padding: "0 6px",
    borderRadius: 4,
    fontSize: fontSizes.sm,
    lineHeight: "18px",
    color: typeColor,
    background: typeColor + "1f",
    border: "1px solid " + typeColor + "55",
  }

  return (
    <div style={{ flex: 1, width: "100%", minWidth: 0, overflowY: "auto", fontSize: fontSizes.base, background: colors.bg }}>
      {/* Sticky header — full width so content scrolling underneath is covered */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          flexWrap: "wrap",
          position: "sticky",
          top: 0,
          zIndex: 1,
          background: colors.bg,
          borderBottom: "1px solid " + colors.border,
          padding: "12px 14px 8px",
        }}
      >
        <span style={tag}>{evt.type}</span>
        <span style={{ color: colors.text, fontSize: fontSizes.md }}>{evt.worker_id}</span>
        <span
          onClick={onClose}
          className="btn-hover"
          title={t('detail.close.tooltip')}
          style={{
            cursor: "pointer",
            marginLeft: "auto",
            border: "1px solid " + colors.border,
            borderRadius: 4,
            padding: "0 8px",
            color: colors.textDim,
            fontSize: fontSizes.md,
            lineHeight: "20px",
            userSelect: "none",
          }}
        >
          {"\u2715"}
        </span>
      </div>

      {/* Scrollable content */}
      <div style={{ padding: 14 }}>
        {contentText && (
          <div
            className="md-content"
            style={{
              marginBottom: 10,
              padding: "8px 12px",
              borderRadius: 6,
              fontSize: fontSizes.md,
              lineHeight: 1.6,
              ...inputBlockStyle,
            }}
          >
            <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{contentText}</Markdown>
          </div>
        )}

        <div style={{ padding: "10px 12px", background: colors.detailBg, borderRadius: 6, fontSize: fontSizes.base }}>
          <div style={{ display: "grid", gridTemplateColumns: "auto 1fr", gap: "4px 16px", alignItems: "baseline" }}>
            <DetailRow label={t('detail.id')} value={evt.id} colors={colors} />
            <DetailRow label={t('detail.traceId')} value={evt.trace_id || t('detail.none')} colors={colors} />
            <DetailRow label={t('detail.time')} value={`${time} · ${evt.timestamp}`} colors={colors} />
            <DetailRow label={t('detail.target')} value={evt.target_worker_id || t('detail.broadcast')} colors={colors} />
            {recipients && <DetailRow label={t('detail.delivered')} value={recipients.join(", ")} colors={colors} />}
          </div>
          <div style={{ marginTop: 8, borderTop: "1px solid " + colors.detailBorder, paddingTop: 8 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
              <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                {t('detail.payload')}
              </div>
              {hasPayload && (
                <span
                  onClick={() => setWrap(v => !v)}
                  title={t('talk.wrap.tooltip')}
                  style={{ marginLeft: 'auto', cursor: 'pointer', color: colors.accentDim, fontSize: fontSizes.sm, textDecoration: 'underline dotted', userSelect: 'none' }}
                >
                  {t('talk.wrap.toggle')}
                </span>
              )}
            </div>
            {hasPayload ? (
              <PayloadGate json={payloadJSON}>
                <CollapsibleCode code={payloadJSON} language="json" showWrapToggle={false} wrap={wrap} onWrapChange={setWrap} />
              </PayloadGate>
            ) : (
              <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('wd.noPayload')}</div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function DetailRow({ label, value, colors }: { label: string; value: string; colors: import('../theme').Palette }) {
  return (
    <>
      <span style={{ color: colors.detailLabel, fontSize: fontSizes.base }}>{label}</span>
      <span style={{ color: colors.detailValue, fontSize: fontSizes.base, wordBreak: "break-all" }}>{value}</span>
    </>
  )
}
