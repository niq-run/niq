import { useEffect, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { deleteVault, fetchVault, putVault, type VaultEntry } from '../services/api'
import { formStyles } from '../components/Form'
import ViewHeader from '../components/ViewHeader'

// VaultView manages the shared credential store (standalone level: one store for
// every project on this machine).
//
// The list endpoint never returns a value, so a saved credential shows as "set"
// and can only be replaced — the browser never holds a token it did not just
// type. Names may contain slashes (`openai/main`) to group entries; that is a
// view, not a hierarchy, and there are no per-scope permissions.
export default function VaultView() {
  const { colors } = useTheme()
  const { t } = useI18n()
  const fs = formStyles(colors)

  const [entries, setEntries] = useState<VaultEntry[]>([])
  const [loadError, setLoadError] = useState('')
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)

  const reload = () => {
    fetchVault()
      .then((res) => {
        setEntries(res.entries || [])
        setLoadError('')
      })
      .catch((err) => setLoadError(String(err.message || err)))
  }
  useEffect(reload, [])

  const save = async () => {
    const n = name.trim()
    if (!n) {
      setError(t('vault.nameRequired'))
      return
    }
    if (!value) {
      setError(t('vault.valueRequired'))
      return
    }
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await putVault(n, value)
      setValue('')
      setNotice(t('vault.saved', { name: n }))
      reload()
    } catch (err: any) {
      setError(String(err.message || err))
    } finally {
      setBusy(false)
    }
  }

  const remove = async (n: string) => {
    setBusy(true)
    setError('')
    setNotice('')
    try {
      await deleteVault(n)
      reload()
    } catch (err: any) {
      setError(String(err.message || err))
    } finally {
      setBusy(false)
    }
  }

  const replace = (n: string) => {
    setName(n)
    setValue('')
    setNotice(t('vault.replaceHint', { name: n }))
  }

  const button: React.CSSProperties = {
    cursor: 'pointer',
    background: 'transparent',
    color: colors.textDim,
    border: '1px solid ' + colors.border,
    borderRadius: 2,
    padding: '3px 10px',
    fontSize: fontSizes.sm,
  }

  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column' }}>
      <ViewHeader title={t('vault.title')} count={entries.length} />
      <div style={{ padding: '16px 24px', overflow: 'auto' }}>
        <p style={{ margin: '0 0 14px', fontSize: fontSizes.sm, color: colors.textMuted, lineHeight: 1.6 }}>
          {t('vault.description')}
        </p>

        {loadError && <div style={{ color: colors.toolFailed, marginBottom: 10 }}>{loadError}</div>}

        {/* Existing entries: name + metadata + replace/delete. No value is shown
            because the server never sends one. */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginBottom: 22 }}>
          {entries.length === 0 && !loadError && (
            <div style={{ color: colors.textDim, fontSize: fontSizes.sm }}>{t('vault.empty')}</div>
          )}
          {entries.map((e) => (
            <div
              key={e.name}
              style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 10px', background: colors.detailBg, borderRadius: 6 }}
            >
              <code style={{ fontSize: fontSizes.sm, color: colors.text, flex: 1, wordBreak: 'break-all' }}>{e.name}</code>
              <span style={{ fontSize: fontSizes.xs, color: colors.textDim }}>
                {t('vault.set')} · {e.updated_at}
              </span>
              <button style={button} disabled={busy} onClick={() => replace(e.name)}>
                {t('vault.replace')}
              </button>
              <button style={{ ...button, color: colors.toolFailed }} disabled={busy} onClick={() => remove(e.name)}>
                {t('vault.delete')}
              </button>
            </div>
          ))}
        </div>

        {/* Write form. Saving an existing name replaces it. */}
        <div style={{ maxWidth: 520, display: 'flex', flexDirection: 'column', gap: 6 }}>
          <label style={{ fontSize: fontSizes.sm, color: colors.textDim }}>{t('vault.name')}</label>
          <input
            style={fs.input}
            value={name}
            placeholder="openai/main"
            spellCheck={false}
            onChange={(ev) => setName(ev.target.value)}
          />
          <label style={{ fontSize: fontSizes.sm, color: colors.textDim, marginTop: 6 }}>{t('vault.value')}</label>
          <input
            style={fs.input}
            value={value}
            type="password"
            autoComplete="off"
            placeholder={t('vault.valuePlaceholder')}
            onChange={(ev) => setValue(ev.target.value)}
          />
          <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginTop: 8 }}>
            <button
              disabled={busy}
              onClick={save}
              style={{ cursor: 'pointer', background: 'transparent', color: colors.accent, border: '1px solid ' + colors.accent, borderRadius: 2, padding: '4px 14px', fontSize: fontSizes.sm }}
            >
              {t('vault.save')}
            </button>
            <span style={{ fontSize: fontSizes.xs, color: colors.textDim }}>{t('vault.hint')}</span>
          </div>
          {error && <div style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{error}</div>}
          {notice && <div style={{ color: colors.textMuted, fontSize: fontSizes.sm }}>{notice}</div>}
        </div>
      </div>
    </div>
  )
}
