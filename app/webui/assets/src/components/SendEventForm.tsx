import { useMemo, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import type { EventPayload, WatchEntry } from '../types'
import { fetchEventsByRequest, sendWorkerEvent } from '../services/api'

interface SendEventFormProps {
  workerId: string
  watch: WatchEntry[]
}

// Render one form control from a JSON Schema. Supported types: string, number,
// integer, boolean. Arrays render as a comma-separated input kept as an array.
function controlForParam(
  key: string,
  schema: any,
  value: any,
  onChange: (v: any) => void,
  colors: any,
  inputStyle: React.CSSProperties,
) {
  const type = schema?.type
  const desc = schema?.description
  if (type === 'boolean') {
    return (
      <label key={key} style={{ display: 'flex', alignItems: 'center', gap: 8, color: colors.textDim, fontSize: fontSizes.sm }}>
        <input type="checkbox" checked={!!value} onChange={(e) => onChange(e.target.checked)} />
        <span>{key}</span>
        {desc && <span style={{ color: colors.textDimmed, fontStyle: 'italic' }}>{desc}</span>}
      </label>
    )
  }
  if (type === 'array') {
    const items = schema?.items
    const arrType = items?.type
    const text = Array.isArray(value) ? value.join(', ') : (value ?? '')
    return (
      <label key={key} style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
        <span style={{ color: colors.textDim, fontSize: fontSizes.sm }}>
          {key} <span style={{ color: colors.textDimmed }}>({arrType}[]) {desc ? '— ' + desc : ''}</span>
        </span>
        <input
          value={text}
          placeholder={arrType === 'string' ? 'a, b, c' : '1, 2, 3'}
          onChange={(e) => {
            const parts = e.target.value.split(',').map((s) => s.trim()).filter((s) => s !== '')
            onChange(arrType === 'number' || arrType === 'integer' ? parts.map(Number) : parts)
          }}
          style={inputStyle}
        />
      </label>
    )
  }
  // string / number / integer
  return (
    <label key={key} style={{ display: 'flex', flexDirection: 'column', gap: 4, minWidth: 0 }}>
      <span style={{ color: colors.textDim, fontSize: fontSizes.sm }}>
        {key} {desc ? <span style={{ color: colors.textDimmed, fontStyle: 'italic' }}>— {desc}</span> : null}
      </span>
      <input
        type={type === 'number' || type === 'integer' ? 'number' : 'text'}
        value={value ?? ''}
        onChange={(e) => {
          const v = e.target.value
          onChange(type === 'number' || type === 'integer' ? (v === '' ? '' : Number(v)) : v)
        }}
        style={inputStyle}
      />
    </label>
  )
}

// SendEventForm renders a form for one watch entry: the event type plus inputs
// generated from its parameter schema. Sending publishes the event to the
// worker as HIW, with the collected values as the top-level payload.
export default function SendEventForm({ workerId, watch }: SendEventFormProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [selected, setSelected] = useState<string>(watch[0]?.event ?? '')
  const [form, setForm] = useState<Record<string, any>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [done, setDone] = useState('')
  // The last sent exchange, shown below the form: the sent event + the
  // worker's request.completed / request.failed reply (correlated by request id).
  const [sent, setSent] = useState<{ type: string; payload: Record<string, any>; reply?: EventPayload } | null>(null)

  const entry = watch.find((w) => w.event === selected)
  // Schema: entries declare parameters as an object schema
  // {"type":"object","properties":{...}} or, in places, a bare properties map.
  const properties = useMemo(() => {
    const p = entry?.parameters
    if (!p) return {}
    if (p.type === 'object' && p.properties && typeof p.properties === 'object') return p.properties as Record<string, any>
    // Fall back to treating the parameters object itself as a direct schema.
    return p
  }, [entry])

  const inputStyle: React.CSSProperties = {
    flex: 1,
    minWidth: 0,
    padding: '5px 8px',
    fontSize: fontSizes.sm,
    background: colors.bgLight,
    color: colors.text,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
  }

  const pollReply = async (reqId: string): Promise<EventPayload | undefined> => {
    for (let i = 0; i < 12; i++) {
      await new Promise((r) => setTimeout(r, 300))
      const evts = await fetchEventsByRequest(reqId).catch(() => [] as EventPayload[])
      const reply = evts.find((e) => e.type === 'request.completed' || e.type === 'request.failed')
      if (reply) return reply
    }
    return undefined
  }

  const submit = async () => {
    setBusy(true)
    setError('')
    setDone('')
    setSent(null)
    try {
      const reqId = await sendWorkerEvent(workerId, selected, form)
      setDone(t('wd.eventSent'))
      if (reqId) {
        const reply = await pollReply(reqId)
        setSent({ type: selected, payload: { ...form }, reply })
      }
    } catch (e) {
      setError((e as Error)?.message || 'send failed')
    } finally {
      setBusy(false)
    }
  }

  if (watch.length === 0) {
    return <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 8, lineHeight: 1.5 }}>{t('wd.noWatch')}</div>
  }

  return (
    <div style={{ marginTop: 6 }}>
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginBottom: 8 }}>
        {watch.map((w) => (
          <span
            key={w.event}
            onClick={() => { setSelected(w.event); setForm({}); setError(''); setDone(''); setSent(null) }}
            title={w.desc}
            style={{
              cursor: 'pointer', display: 'inline-block', padding: '4px 10px', borderRadius: 2, fontSize: fontSizes.sm, lineHeight: '18px',
              color: selected === w.event ? colors.accent : colors.textDim,
              background: selected === w.event ? colors.accentBg : colors.bgLight,
              border: '1px solid ' + (selected === w.event ? colors.accentBorder : colors.border),
              userSelect: 'none', whiteSpace: 'nowrap',
            }}
          >
            {w.event}
          </span>
        ))}
      </div>
      {entry && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {entry.desc && <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginBottom: 4 }}>{entry.desc}</div>}
          {Object.keys(properties).length === 0 ? (
            <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed }}>{t('wd.eventNoParams')}</div>
          ) : (
            Object.entries(properties).map(([k, schema]) =>
              controlForParam(k, schema, form[k], (v) => setForm((f) => ({ ...f, [k]: v })), colors, inputStyle),
            )
          )}
          <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
            <span
              onClick={submit}
              className="btn-hover"
              style={{ cursor: busy ? 'default' : 'pointer', opacity: busy ? 0.6 : 1, display: 'inline-block', border: '1px solid ' + colors.accentBorder, borderRadius: 4, padding: '4px 12px', color: colors.accent, fontSize: fontSizes.md, userSelect: 'none' }}
            >
              {busy ? t('wd.sending') : t('wd.send')}
            </span>
            {done && <span style={{ color: colors.toolCompleted, fontSize: fontSizes.sm }}>{done}</span>}
            {error && <span style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{error}</span>}
          </div>
          {/* Sent event + its reply, shown right under the form after sending. */}
          {sent && (
            <div style={{ borderTop: '1px solid ' + colors.border, paddingTop: 10, display: 'flex', flexDirection: 'column', gap: 8 }}>
              <div style={{ fontSize: fontSizes.sm, color: colors.textDim }}>
                {t('wd.eventSentPreview')} <strong style={{ color: colors.text }}>{sent.type}</strong>
              </div>
              {Object.keys(sent.payload).length > 0 && (
                <pre style={{ margin: 0, padding: 8, background: colors.bgLight, border: '1px solid ' + colors.border, borderRadius: 4, fontSize: fontSizes.xs, color: colors.text, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{JSON.stringify(sent.payload, null, 2)}</pre>
              )}
              <div style={{ fontSize: fontSizes.sm, color: colors.textDim }}>{t('wd.eventResponse')}:</div>
              {sent.reply ? (
                <pre
                  style={{ margin: 0, padding: 8, background: colors.bgLight, border: '1px solid ' + (sent.reply.type === 'request.failed' ? '#c55' : colors.accentBorder), borderRadius: 4, fontSize: fontSizes.xs, color: sent.reply.type === 'request.failed' ? '#c55' : colors.text, whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}
                >
                  {JSON.stringify(sent.reply.payload ?? {}, null, 2)}
                </pre>
              ) : (
                <span style={{ fontSize: fontSizes.sm, color: colors.textDimmed }}>{t('wd.eventWaiting')}</span>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}