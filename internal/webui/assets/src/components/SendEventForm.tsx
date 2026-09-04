import { useMemo, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import type { WatchEntry } from '../types'
import { sendWorkerEvent } from '../services/api'

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

  const submit = async () => {
    setBusy(true)
    setError('')
    setDone('')
    try {
      await sendWorkerEvent(workerId, selected, form)
      setDone(t('wd.eventSent'))
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
            onClick={() => { setSelected(w.event); setForm({}); setError(''); setDone('') }}
            title={w.desc}
            style={{
              cursor: 'pointer', display: 'inline-block', padding: '2px 9px', borderRadius: 4, fontSize: fontSizes.sm,
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
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {entry.desc && <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed }}>{entry.desc}</div>}
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
              style={{ cursor: busy ? 'default' : 'pointer', opacity: busy ? 0.6 : 1, display: 'inline-block', border: '1px solid ' + colors.accentBorder, borderRadius: 4, padding: '3px 12px', color: colors.accent, fontSize: fontSizes.sm, userSelect: 'none' }}
            >
              {busy ? t('wd.sending') : t('wd.send')}
            </span>
            {done && <span style={{ color: colors.toolCompleted, fontSize: fontSizes.sm }}>{done}</span>}
            {error && <span style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{error}</span>}
          </div>
        </div>
      )}
    </div>
  )
}