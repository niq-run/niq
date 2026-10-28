import { useEffect, useRef, useState } from 'react'
import { useTheme, fontSizes, type Palette } from '../theme'
import { useI18n } from '../i18n'
import type { WorkerInfo } from '../types'
import { BUILTIN_EVENT_TYPES } from '../eventTypes'

// PublishRow / SubscribeRow are the editable row shapes of the two allow lists.
// An empty target/source means "any", and the rows map 1:1 onto the worker
// declaration's `publish` / `subscriptions` entries, so the same editors serve
// the create form (draft rows) and the detail panel (rows saved via the API).
export interface PublishRow {
  type: string
  target: string
}

export interface SubscribeRow {
  type: string
  source: string
}

// controlStyle is the shared look of an allow-row control: the row inputs and
// selects line up with the detail panel's other form controls.
function controlStyle(colors: Palette, disabled: boolean): React.CSSProperties {
  return {
    flex: 1,
    minWidth: 0,
    padding: '5px 8px',
    fontSize: fontSizes.sm,
    background: colors.bgLight,
    color: colors.text,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
    opacity: disabled ? 0.75 : 1,
  }
}

// dropdownButtonStyle is the standalone trigger button that belongs to a text
// field (event type input): a bordered button of the field's own height rather
// than a tiny glyph inside the input.
function dropdownButtonStyle(colors: Palette, disabled = false): React.CSSProperties {
  return {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    flexShrink: 0,
    minWidth: 30,
    padding: '0 7px',
    fontSize: fontSizes.sm,
    color: colors.textDim,
    background: colors.bgLight,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
    cursor: disabled ? 'default' : 'pointer',
    opacity: disabled ? 0.5 : 1,
    userSelect: 'none',
    boxSizing: 'border-box',
  }
}

// optionPanelStyle is the floating option list every dropdown in this file
// shares.
function optionPanelStyle(colors: Palette): React.CSSProperties {
  return {
    position: 'absolute',
    left: 0,
    top: '100%',
    marginTop: 4,
    width: '100%',
    maxHeight: 220,
    overflowY: 'auto',
    background: colors.bgLight,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
    boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
    zIndex: 30,
  }
}

// useOutsideClose closes a popup on any click outside `ref`.
function useOutsideClose(ref: React.RefObject<HTMLDivElement | null>, open: boolean, close: () => void) {
  // Keep the callback in a ref so the listener is bound once per open state,
  // not re-bound on every keystroke that re-renders the field.
  const closeRef = useRef(close)
  closeRef.current = close
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) closeRef.current()
    }
    window.addEventListener('click', handler)
    return () => window.removeEventListener('click', handler)
  }, [open, ref])
}

// EventTypeField is a free-text event-type input with a picker button that
// offers the built-in bus-protocol types: typing worker-specific types
// (reason.*, timer.*, …) stays possible, while the common ones are one click
// away. Used for publish grants, which any worker may publish.
function EventTypeField({ value, onChange, options, placeholder, disabled }: {
  value: string
  onChange: (v: string) => void
  options: readonly string[]
  placeholder?: string
  disabled?: boolean
}) {
  const { colors } = useTheme()
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)
  useOutsideClose(wrapRef, open, () => setOpen(false))

  return (
    <div ref={wrapRef} style={{ position: 'relative', display: 'flex', gap: 4, alignItems: 'stretch', flex: 1, minWidth: 0 }}>
      <input
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        style={{ ...controlStyle(colors, !!disabled), width: '100%', boxSizing: 'border-box' }}
      />
      {!disabled && (
        <span
          onClick={() => setOpen((v) => !v)}
          title={placeholder}
          style={dropdownButtonStyle(colors)}
        >
          {open ? '▴' : '▾'}
        </span>
      )}
      {open && (
        <div style={optionPanelStyle(colors)}>
          {options.map((o) => (
            <div
              key={o}
              onClick={() => { onChange(o); setOpen(false) }}
              style={{
                cursor: 'pointer',
                padding: '6px 9px',
                fontSize: fontSizes.sm,
                whiteSpace: 'nowrap',
                color: o === value ? colors.accent : colors.text,
                background: o === value ? colors.bgChip : undefined,
              }}
            >
              {o}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// EventSelect is a dropdown-only event-type field, used for subscribe grants:
// the available events are exactly what the chosen source worker publishes, so
// there is nothing to type. With no source chosen it cannot be opened — the
// event list is unknown until a source is picked.
function EventSelect({ value, options, onChange, placeholder, disabled }: {
  value: string
  options: string[]
  onChange: (v: string) => void
  placeholder: string
  disabled?: boolean
}) {
  const { colors } = useTheme()
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)
  const close = () => setOpen(false)
  useOutsideClose(wrapRef, open, close)

  const canOpen = !disabled && options.length > 0

  return (
    <div ref={wrapRef} style={{ position: 'relative', flex: 1, minWidth: 0 }}>
      <div
        onClick={canOpen ? () => setOpen((v) => !v) : undefined}
        title={placeholder}
        style={{
          ...controlStyle(colors, !canOpen),
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: 6,
          boxSizing: 'border-box',
          cursor: canOpen ? 'pointer' : 'default',
          color: value !== '' ? colors.text : colors.textDimmed,
          userSelect: 'none',
          minHeight: 26,
        }}
      >
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {value !== '' ? value : placeholder}
        </span>
        <span style={{ flexShrink: 0, fontSize: fontSizes.xs, color: colors.textDimmed }}>{open ? '▴' : '▾'}</span>
      </div>
      {open && (
        <div style={optionPanelStyle(colors)}>
          {options.map((o) => (
            <div
              key={o}
              onClick={() => { onChange(o); setOpen(false) }}
              style={{
                cursor: 'pointer',
                padding: '6px 9px',
                fontSize: fontSizes.sm,
                whiteSpace: 'nowrap',
                color: o === value ? colors.accent : colors.text,
                background: o === value ? colors.bgChip : undefined,
              }}
            >
              {o}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// WorkerSelect is the target / source dropdown shared by both row editors: the
// caller's "any" value first (publish uses "" for any target, subscribe uses
// the canonical "*" for any source), then every known worker, plus the row's
// current value when it names a worker the list does not know (offline /
// deleted).
function WorkerSelect({ value, allWorkers, anyValue = '', anyLabel, onChange, disabled, width }: {
  value: string
  allWorkers: WorkerInfo[]
  anyValue?: string
  anyLabel: string
  onChange: (v: string) => void
  disabled?: boolean
  width: number
}) {
  const { colors } = useTheme()
  const style: React.CSSProperties = { ...controlStyle(colors, !!disabled), flex: `0 1 ${width}px`, appearance: 'none' }
  return (
    <select value={value || anyValue} disabled={disabled} onChange={(e) => onChange(e.target.value)} style={style}>
      <option value={anyValue} style={{ background: colors.bgLight, color: colors.text }}>{anyLabel}</option>
      {value !== '' && value !== anyValue && !allWorkers.some((w) => w.id === value) && (
        <option value={value} style={{ background: colors.bgLight, color: colors.text }}>{value}</option>
      )}
      {allWorkers.map((w) => (
        <option key={w.id} value={w.id} style={{ background: colors.bgLight, color: colors.text }}>{w.id}</option>
      ))}
    </select>
  )
}

// AddButton is the small "+ add" pill both editors use.
function AddButton({ label, onClick, disabled }: { label: string; onClick: () => void; disabled?: boolean }) {
  const { colors } = useTheme()
  return (
    <span
      onClick={disabled ? undefined : onClick}
      className="btn-hover"
      style={{
        cursor: disabled ? 'default' : 'pointer',
        opacity: disabled ? 0.5 : 1,
        display: 'inline-block',
        border: '1px solid ' + colors.border,
        borderRadius: 2,
        padding: '3px 10px',
        color: colors.textDim,
        fontSize: fontSizes.sm,
        userSelect: 'none',
        whiteSpace: 'nowrap',
      }}
    >
      {label}
    </span>
  )
}

// PublishAllowRows edits/renders the publish grants. A row reads left to right
// as "target ← event": who may receive it, then which event type.
export function PublishAllowRows({ rows, onChange, allWorkers = [], disabled = false }: {
  rows: PublishRow[]
  onChange: (rows: PublishRow[]) => void
  allWorkers?: WorkerInfo[]
  disabled?: boolean
}) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const update = (i: number, patch: Partial<PublishRow>) =>
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))

  return (
    <div style={{ marginTop: 6 }}>
      {rows.map((r, i) => (
        <div key={i} style={{ display: 'flex', gap: 6, marginBottom: 6, alignItems: 'stretch' }}>
          <WorkerSelect
            value={r.target}
            allWorkers={allWorkers}
            anyLabel={t('wd.anyTarget')}
            width={170}
            disabled={disabled}
            onChange={(v) => update(i, { target: v })}
          />
          <EventTypeField
            value={r.type}
            onChange={(v) => update(i, { type: v })}
            options={BUILTIN_EVENT_TYPES}
            placeholder={disabled ? undefined : t('wd.pubPlaceholder')}
            disabled={disabled}
          />
          {!disabled && (
            <span
              onClick={() => onChange(rows.filter((_, j) => j !== i))}
              className="btn-hover"
              title={t('wd.remove')}
              style={{ cursor: 'pointer', color: colors.textDimmed, fontSize: fontSizes.md, userSelect: 'none', padding: '0 4px', alignSelf: 'center' }}
            >
              {'\u2715'}
            </span>
          )}
        </div>
      ))}
      {!disabled && (
        <AddButton label={t('wd.add')} onClick={() => onChange([...rows, { type: '', target: '' }])} />
      )}
      {rows.length === 0 && (
        <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 8, lineHeight: 1.5 }}>
          {t('wd.noPublish')}
        </div>
      )}
    </div>
  )
}

// SubscribeAllowRows edits/renders the subscription grants. A row reads left to
// right as "source → event": pick the worker that publishes it, then pick one
// of the events that worker actually publishes (the list comes from its
// publish grants, so no typing is possible — and no list exists until a source
// is chosen).
export function SubscribeAllowRows({ rows, onChange, allWorkers = [], disabled = false }: {
  rows: SubscribeRow[]
  onChange: (rows: SubscribeRow[]) => void
  allWorkers?: WorkerInfo[]
  disabled?: boolean
}) {
  const { colors } = useTheme()
  const { t } = useI18n()

  const update = (i: number, patch: Partial<SubscribeRow>) =>
    onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))

  // The events a given source worker publishes, de-duplicated. "*"/"" is
  // "any source" — no worker to ask — and an unknown source (offline / not in
  // the list) yields none.
  const eventsOf = (sourceId: string): string[] => {
    if (sourceId === '' || sourceId === '*') return []
    const w = allWorkers.find((x) => x.id === sourceId)
    return [...new Set((w?.publish_allow || []).map((p) => p.type))].filter(Boolean)
  }

  return (
    <div style={{ marginTop: 6 }}>
      {rows.map((r, i) => {
        // "*" (and the legacy empty value) means "any source".
        const source = r.source || '*'
        const sourced = source !== '*'
        // With a specific source the event list is whatever that worker
        // publishes (no typing). An "any source" row has no worker to ask, so it
        // keeps the free-text field + built-in types, exactly like publish.
        const events = eventsOf(source)
        // Keep the row's own type selectable even when the worker's list does
        // not carry it any more (a grant persisted before the worker changed).
        const options = r.type !== '' && !events.includes(r.type) ? [...events, r.type] : events
        return (
          <div key={i} style={{ display: 'flex', gap: 6, marginBottom: 6, alignItems: 'stretch' }}>
            <WorkerSelect
              value={source}
              allWorkers={allWorkers}
              anyValue="*"
              anyLabel={t('wd.anySource')}
              width={170}
              disabled={disabled}
              // Changing the source invalidates the event picked from the old
              // one: reset it so the dropdown re-bases on the new worker.
              onChange={(v) => update(i, { source: v, ...(v !== source ? { type: '' } : {}) })}
            />
            {sourced ? (
              <EventSelect
                value={r.type}
                options={options}
                placeholder={options.length === 0 ? t('wd.allow.noWorkerEvents') : t('wd.allow.pickEvent')}
                disabled={disabled}
                onChange={(v) => update(i, { type: v })}
              />
            ) : (
              <EventTypeField
                value={r.type}
                options={BUILTIN_EVENT_TYPES}
                placeholder={disabled ? undefined : t('wd.subPlaceholder')}
                disabled={disabled}
                onChange={(v) => update(i, { type: v })}
              />
            )}
            {!disabled && (
              <span
                onClick={() => onChange(rows.filter((_, j) => j !== i))}
                className="btn-hover"
                title={t('wd.remove')}
                style={{ cursor: 'pointer', color: colors.textDimmed, fontSize: fontSizes.md, userSelect: 'none', padding: '0 4px', alignSelf: 'center' }}
              >
                {'\u2715'}
              </span>
            )}
          </div>
        )
      })}
      {!disabled && (
        <AddButton label={t('wd.add')} onClick={() => onChange([...rows, { type: '', source: '*' }])} />
      )}
      {rows.length === 0 && (
        <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 8, lineHeight: 1.5 }}>
          {t('wd.noSubscriptions')}
        </div>
      )}
    </div>
  )
}
