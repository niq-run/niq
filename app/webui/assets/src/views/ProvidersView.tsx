import { useEffect, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { fetchProviders, updateProviders } from '../services/api'
import JsonEditor from '../components/JsonEditor'
import ViewHeader from '../components/ViewHeader'
import BufferedInput, { envToText, textToMap } from '../components/BufferedInput'

// ProvidersView manages the provider config (provider.json from the common
// config layer) with the same dual view as the template editor: worker-style
// cards over the common fields, plus the raw JSON. Providers are referenced by
// name (the config's active field and runtime provider switches), so names
// must be unique. All calls go to the control plane, same-origin (CONTROL).
export default function ProvidersView() {
  const { dark, colors } = useTheme()
  const { t } = useI18n()
  const [cfg, setCfg] = useState<any>(null)
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState('')
  const [saved, setSaved] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [editorTab, setEditorTab] = useState<'visual' | 'json'>('visual')

  useEffect(() => {
    fetchProviders()
      .then((c) => openConfig(c))
      .catch(() => setSaveError(t('providers.error.load')))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const openConfig = (c: any) => {
    const body = { active: c.active || '', providers: c.providers || [] }
    setCfg(body)
    setJsonText(JSON.stringify(body, null, 2))
    setJsonError('')
    setSaveError('')
    setSaved(false)
  }

  // applyConfig is the visual-edit exit: update the canonical object and
  // regenerate the JSON text.
  const applyConfig = (next: any) => {
    setCfg(next)
    setJsonText(JSON.stringify(next, null, 2))
    setJsonError('')
    setSaved(false)
  }

  const onJsonChange = (text: string) => {
    setJsonText(text)
    setSaved(false)
    try {
      setCfg(JSON.parse(text))
      setJsonError('')
    } catch {
      setJsonError(t('templates.error.json'))
    }
  }

  const updateProvider = (i: number, patch: any) => {
    if (!cfg || jsonError) return
    const providers = cfg.providers.map((p: any, j: number) => (j === i ? { ...p, ...patch } : p))
    applyConfig({ ...cfg, providers })
  }

  const removeProvider = (i: number) => {
    if (!cfg || jsonError) return
    const providers = cfg.providers.filter((_: any, j: number) => j !== i)
    const next = { ...cfg, providers }
    // Dropping the active provider must not leave a stale reference.
    if (next.active && !providers.some((p: any) => p.name === next.active)) next.active = ''
    applyConfig(next)
  }

  // Model rows keep per-model metadata (context window, max output, input
  // modalities) as objects; the backend re-marshals the list back to plain
  // names when no entry carries metadata, so the stored file stays compact.
  type ModelFields = { name: string; context_window?: number; max_output?: number; input_types?: string[] }
  const modelFields = (m: any): ModelFields => (typeof m === 'string'
    ? { name: m }
    : { name: m.name || '', context_window: m.context_window, max_output: m.max_output, input_types: m.input_types })
  const setModel = (pi: number, mi: number, patch: Partial<ModelFields>) => {
    if (!cfg || jsonError) return
    const models = [...(cfg.providers[pi].models || [])]
    const next: ModelFields = { ...modelFields(models[mi]), ...patch }
    const row: any = { name: next.name }
    if (next.context_window) row.context_window = next.context_window
    if (next.max_output) row.max_output = next.max_output
    if (next.input_types && next.input_types.length > 0) row.input_types = next.input_types
    models[mi] = row
    applyConfig({ ...cfg, providers: cfg.providers.map((p: any, j: number) => (j === pi ? { ...p, models } : p)) })
  }
  // Modalities are a token list; "text" is always implied, and clearing every
  // box means "unknown" (the reader then assumes the model accepts anything).
  const toggleModality = (pi: number, mi: number, m: any, modality: string) => {
    const cur = modelFields(m).input_types ?? []
    const has = cur.includes(modality) || cur.includes('multimodal')
    const next = has ? cur.filter((t) => t !== modality) : [...new Set([...cur, 'text', modality])]
    setModel(pi, mi, { input_types: next })
  }
  const removeModel = (pi: number, mi: number) => {
    if (!cfg || jsonError) return
    const models = (cfg.providers[pi].models || []).filter((_: any, j: number) => j !== mi)
    applyConfig({ ...cfg, providers: cfg.providers.map((p: any, j: number) => (j === pi ? { ...p, models } : p)) })
  }
  const addModel = (pi: number) => {
    if (!cfg || jsonError) return
    const models = [...(cfg.providers[pi].models || []), { name: '' }]
    applyConfig({ ...cfg, providers: cfg.providers.map((p: any, j: number) => (j === pi ? { ...p, models } : p)) })
  }

  const addProvider = () => {
    if (!cfg || jsonError) return
    const providers = [...(cfg.providers || []), { name: '', type: 'openai-compatible' }]
    applyConfig({ ...cfg, providers })
  }

  const save = async () => {
    if (!cfg || jsonError) return
    setSaveError('')
    try {
      await updateProviders(cfg)
      setSaved(true)
    } catch (e) {
      setSaved(false)
      setSaveError((e as Error)?.message || t('providers.error.save'))
    }
  }

  const cardInput: React.CSSProperties = {
    width: '100%',
    boxSizing: 'border-box',
    padding: '5px 8px',
    fontSize: fontSizes.sm,
    background: colors.bgLight,
    color: colors.text,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
    outline: 'none',
  }
  const cardLabel: React.CSSProperties = { display: 'block', fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 4 }
  const field = (label: string, node: any, key?: string, style?: React.CSSProperties) => (
    <div key={key} style={style}>
      <label style={cardLabel}>{label}</label>
      {node}
    </div>
  )

  return (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      <ViewHeader title={t('providers.title')} count={cfg?.providers?.length ?? 0} />

      {/* Tabs: visual cards and the raw JSON file, plus save on the right. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '12px 24px 14px', flexShrink: 0 }}>
        {(['visual', 'json'] as const).map((tab) => (
          <span
            key={tab}
            onClick={() => setEditorTab(tab)}
            style={{
              cursor: 'pointer',
              userSelect: 'none',
              border: '1px solid ' + (editorTab === tab ? colors.accent : colors.border),
              borderRadius: 2,
              padding: '3px 12px',
              fontSize: fontSizes.sm,
              color: editorTab === tab ? colors.accent : colors.textDim,
              background: editorTab === tab ? colors.bgChip : undefined,
            }}
          >
            {tab === 'visual' ? t('providers.tab.visual') : t('providers.tab.json')}
          </span>
        ))}
        <span style={{ flex: 1 }} />
        {saved && (
          <span style={{ color: colors.textDim, fontSize: fontSizes.sm }}>✓ {t('providers.saved')}</span>
        )}
        {!saved && saveError && (
          <span title={saveError} style={{ color: colors.toolFailed, fontSize: fontSizes.sm, maxWidth: '45%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{saveError}</span>
        )}
        <span
          onClick={save}
          className="btn-hover"
          style={{
            cursor: jsonError ? 'not-allowed' : 'pointer',
            border: '1px solid ' + (jsonError ? colors.border : colors.accent),
            borderRadius: 4,
            padding: '3px 12px',
            color: jsonError ? colors.textDimmed : colors.accent,
            fontSize: fontSizes.sm,
            userSelect: 'none',
          }}
        >
          {t('templates.save')}
        </span>
      </div>

      <div style={{ flex: 1, minWidth: 0, overflowY: 'auto', padding: '12px 24px 24px', display: 'flex', flexDirection: 'column', gap: 12 }}>
        {editorTab === 'visual' && cfg && (
          <>
            {/* Active provider: names the default the workers start on. */}
            {field(t('providers.field.active'), (
              <select
                value={cfg.active || ''}
                onChange={(e) => applyConfig({ ...cfg, active: e.target.value })}
                style={{ width: 280, ...cardInput }}
              >
                <option value="">{t('providers.field.activeNone')}</option>
                {(cfg.providers || []).map((p: any, i: number) => (
                  <option key={i} value={p.name}>{p.name || `#${i + 1}`}</option>
                ))}
              </select>
            ))}
            {(cfg.providers || []).length === 0 && (
              <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('providers.noProviders')}</div>
            )}
            {(cfg.providers || []).map((p: any, i: number) => (
              <div key={i + ':' + (p.name || '')} style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 8, background: colors.detailBg }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span
                    onClick={() => applyConfig({ ...cfg, active: p.name })}
                    title={t('providers.field.active')}
                    style={{ cursor: 'pointer', color: cfg.active === p.name ? colors.accent : colors.textDimmed, fontSize: fontSizes.md }}
                  >
                    {cfg.active === p.name ? '◉' : '○'}
                  </span>
                  <span style={{ color: colors.accent, fontSize: fontSizes.md, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{p.name || t('providers.unnamed')}</span>
                  <span style={{ flex: 1 }} />
                  <button
                    onClick={() => removeProvider(i)}
                    style={{ cursor: 'pointer', background: 'transparent', color: colors.toolFailed, border: '1px solid ' + colors.border, borderRadius: 2, padding: '3px 10px', fontSize: fontSizes.sm }}
                  >
                    {t('templates.delete')}
                  </button>
                </div>
                <div style={{ display: 'flex', gap: 8 }}>
                  {field(t('providers.field.name'), (
                    <input value={p.name || ''} onChange={(e) => updateProvider(i, { name: e.target.value })} style={cardInput} />
                  ), 'n', { flex: 1, minWidth: 0 })}
                  {field(t('providers.field.type'), (
                    <input value={p.type || ''} onChange={(e) => updateProvider(i, { type: e.target.value })} placeholder="openai-compatible" style={cardInput} />
                  ), 't', { flex: 1, minWidth: 0 })}
                </div>
                {field(t('providers.field.baseUrl'), (
                  <input value={p.base_url || ''} onChange={(e) => updateProvider(i, { base_url: e.target.value })} placeholder="https://api.example.com/v1" style={cardInput} />
                ))}
                <div style={{ display: 'flex', gap: 8 }}>
                  {field(t('providers.field.apiKey'), (
                    <input value={p.api_key || ''} onChange={(e) => updateProvider(i, { api_key: e.target.value })} placeholder="sk-… 或 ${VAR}" style={cardInput} />
                  ), 'k', { flex: 1, minWidth: 0 })}
                  {field(t('providers.field.model'), (
                    <input value={p.model || ''} onChange={(e) => updateProvider(i, { model: e.target.value })} style={cardInput} />
                  ), 'm', { flex: 1, minWidth: 0 })}
                </div>
                {field(t('providers.field.models'), (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                    {(Array.isArray(p.models) ? p.models : []).map((m: any, j: number) => {
                      const f = modelFields(m)
                      return (
                      <div key={j} style={{ display: 'flex', flexDirection: 'column', gap: 6, border: '1px solid ' + colors.border, borderRadius: 3, padding: '6px 8px' }}>
                        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                          <input
                            value={f.name}
                            onChange={(e) => setModel(i, j, { name: e.target.value })}
                            placeholder="model-name"
                            style={{ ...cardInput, flex: 1, minWidth: 0 }}
                          />
                          <input
                            value={f.context_window || ''}
                            onChange={(e) => {
                              const n = parseInt(e.target.value, 10)
                              setModel(i, j, { context_window: Number.isFinite(n) && n > 0 ? n : undefined })
                            }}
                            placeholder={t('providers.field.contextWindow')}
                            style={{ ...cardInput, width: 150, flexShrink: 0 }}
                          />
                          <input
                            value={f.max_output || ''}
                            onChange={(e) => {
                              const n = parseInt(e.target.value, 10)
                              setModel(i, j, { max_output: Number.isFinite(n) && n > 0 ? n : undefined })
                            }}
                            placeholder={t('providers.field.maxOutput')}
                            style={{ ...cardInput, width: 140, flexShrink: 0 }}
                          />
                          <button
                            onClick={() => removeModel(i, j)}
                            style={{ cursor: 'pointer', background: 'transparent', color: colors.textDim, border: '1px solid ' + colors.border, borderRadius: 2, padding: '3px 8px', fontSize: fontSizes.sm, flexShrink: 0 }}
                          >
                            ✕
                          </button>
                        </div>
                        <div style={{ display: 'flex', gap: 12, alignItems: 'center', fontSize: fontSizes.xs, color: colors.textDim, flexWrap: 'wrap' }}>
                          <span>{t('providers.field.inputs')}</span>
                          {(['image', 'audio', 'video'] as const).map((mod) => {
                            const label = {
                              image: 'providers.modality.image',
                              audio: 'providers.modality.audio',
                              video: 'providers.modality.video',
                            } as const
                            const types = f.input_types ?? []
                            const on = types.includes(mod) || types.includes('multimodal')
                            return (
                              <label key={mod} style={{ display: 'inline-flex', alignItems: 'center', gap: 4, cursor: 'pointer', userSelect: 'none' }}>
                                <input type="checkbox" checked={on} onChange={() => toggleModality(i, j, m, mod)} />
                                {t(label[mod])}
                              </label>
                            )
                          })}
                          <span style={{ color: colors.textDimmed }}>{t('providers.field.inputsHint')}</span>
                        </div>
                      </div>
                      )
                    })}
                    <span
                      onClick={() => addModel(i)}
                      style={{ cursor: 'pointer', color: colors.accent, fontSize: fontSizes.sm, textDecoration: 'underline dotted', userSelect: 'none' }}
                    >
                      + {t('providers.addModel')}
                    </span>
                  </div>
                ), 'ms')}
                {field(t('providers.field.contextWindow'), (
                  <input
                    value={p.context_window || ''}
                    onChange={(e) => {
                      const n = parseInt(e.target.value, 10)
                      updateProvider(i, { context_window: Number.isFinite(n) && n > 0 ? n : undefined })
                    }}
                    placeholder="128000"
                    style={cardInput}
                  />
                ), 'cw')}
                {field(t('providers.field.headers'), (
                  <BufferedInput
                    value={envToText(p.headers)}
                    onText={(text) => {
                      const headers = textToMap(text)
                      updateProvider(i, { headers: Object.keys(headers).length > 0 ? headers : undefined })
                    }}
                    as="textarea"
                    style={{ ...cardInput, fontFamily: 'monospace', resize: 'vertical', minHeight: 48 }}
                  />
                ))}
              </div>
            ))}
            <div>
              <span
                onClick={addProvider}
                className="btn-hover"
                style={{ cursor: 'pointer', display: 'inline-block', fontSize: fontSizes.sm, color: colors.accent, border: '1px solid ' + colors.accent, borderRadius: 2, padding: '3px 12px', userSelect: 'none' }}
              >
                + {t('providers.add')}
              </span>
            </div>
          </>
        )}

        {editorTab === 'json' && cfg && (
          <>
            <JsonEditor value={jsonText} onChange={onJsonChange} dark={dark} colors={colors} />
            {jsonError && <div style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{jsonError}</div>}
          </>
        )}
      </div>
    </div>
  )
}
