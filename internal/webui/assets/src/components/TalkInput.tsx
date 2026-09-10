import { useState, useRef, useEffect, useMemo } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import PickerDropdown, { type PickerOption } from './PickerDropdown'
import { uploadFile } from '../services/api'
import type { StagedAttachment, WorkerInfo } from '../types'

// Attachment limits: images ride inline as base64, files go through
// /api/upload and only their path enters the input.
const MAX_ATTACHMENTS = 4
const MAX_IMAGE_BYTES = 5 * 1024 * 1024
// Images above this edge length are downscaled before embedding — the
// useful resolution ceiling of mainstream vision models, and a large win
// for event/snapshot size.
const IMAGE_MAX_DIM = 1568
const IMAGE_JPEG_QUALITY = 0.85

interface TalkInputProps {
  talkPartner: string
  input: string
  inputMode: string
  onInputChange: (v: string) => void
  onSend: () => void
  onAbort: () => void
  onModeChange: (m: string) => void
  workers: WorkerInfo[]
  archived: Set<string>
  mentionKey: number
  mentionTarget: string
  onClearMentionTarget: () => void
  onSelectTarget: (id: string) => void
  isMobile: boolean
  // Attachments staged for the next send: pasted/picked images (inline
  // base64) and uploaded files (referenced by path).
  attachments: StagedAttachment[]
  onAttachmentsChange: (a: StagedAttachment[]) => void
}

export default function TalkInput({ talkPartner, input, inputMode, onInputChange, onSend, onAbort, onModeChange, workers, archived, mentionKey, mentionTarget, onClearMentionTarget, onSelectTarget, isMobile, attachments, onAttachmentsChange }: TalkInputProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [pickerMode, setPickerMode] = useState<'mention' | 'target'>('target')
  const [mentionQuery, setMentionQuery] = useState('')
  const [mentionIndex, setMentionIndex] = useState(0)
  const [modeOpen, setModeOpen] = useState(false)
  const [uploading, setUploading] = useState(0)
  const [attachNote, setAttachNote] = useState('')

  const reasonWorkers = useMemo(() => workers.filter(w => w.type === 'reason'), [workers])

  // ── Attachments ──
  const stage = (a: StagedAttachment) => {
    onAttachmentsChange([...attachments, a])
    setAttachNote('')
  }

  const readAsDataURL = (file: File | Blob) =>
    new Promise<string>((resolve, reject) => {
      const r = new FileReader()
      r.onload = () => resolve(r.result as string)
      r.onerror = () => reject(r.error)
      r.readAsDataURL(file)
    })

  const loadImage = (url: string) =>
    new Promise<HTMLImageElement>((resolve, reject) => {
      const img = new Image()
      img.onload = () => resolve(img)
      img.onerror = reject
      img.src = url
    })

  const canvasToDataURL = (canvas: HTMLCanvasElement, mime: string, quality?: number) =>
    new Promise<string>((resolve, reject) => {
      canvas.toBlob(
        (blob) => {
          if (!blob) { reject(new Error('canvas encode failed')); return }
          const r = new FileReader()
          r.onload = () => resolve(r.result as string)
          r.onerror = () => reject(r.error)
          r.readAsDataURL(blob)
        },
        mime,
        quality,
      )
    })

  // compressImage shrinks a picked/pasted image before it rides inline:
  // downscale past IMAGE_MAX_DIM, re-encode (JPEG q0.85 for non-PNG; PNG
  // keeps its alpha and only falls back to JPEG if still over the cap).
  // Already-small images pass through untouched.
  const compressImage = async (file: File): Promise<{ data: string; mime: string; bytes: number }> => {
    const url = await readAsDataURL(file)
    const img = await loadImage(url)
    const scale = Math.min(1, IMAGE_MAX_DIM / Math.max(img.naturalWidth, img.naturalHeight))
    if (scale === 1 && file.size <= MAX_IMAGE_BYTES) {
      return { data: url.split(',')[1] || '', mime: file.type, bytes: file.size }
    }

    const keepPng = file.type === 'image/png'
    const canvas = document.createElement('canvas')
    canvas.width = Math.max(1, Math.round(img.naturalWidth * scale))
    canvas.height = Math.max(1, Math.round(img.naturalHeight * scale))
    const ctx = canvas.getContext('2d')!
    if (!keepPng) {
      // JPEG has no alpha: fill white before drawing.
      ctx.fillStyle = '#fff'
      ctx.fillRect(0, 0, canvas.width, canvas.height)
    }
    ctx.drawImage(img, 0, 0, canvas.width, canvas.height)

    if (keepPng) {
      const pngURL = await canvasToDataURL(canvas, 'image/png')
      const bytes = Math.round((pngURL.length - pngURL.indexOf(',')) * 0.75)
      if (bytes <= MAX_IMAGE_BYTES) {
        return { data: pngURL.split(',')[1] || '', mime: 'image/png', bytes }
      }
      // Still too big: flatten onto white and encode as JPEG.
      ctx.fillStyle = '#fff'
      ctx.fillRect(0, 0, canvas.width, canvas.height)
      ctx.drawImage(img, 0, 0, canvas.width, canvas.height)
    }
    const url2 = await canvasToDataURL(canvas, 'image/jpeg', IMAGE_JPEG_QUALITY)
    return { data: url2.split(',')[1] || '', mime: 'image/jpeg', bytes: Math.round((url2.length - url2.indexOf(',')) * 0.75) }
  }

  // addFiles stages picked/pasted files: images inline as base64 (capped),
  // everything else via the upload endpoint (path reference).
  const addFiles = async (files: FileList | File[]) => {
    for (const f of Array.from(files)) {
      if (attachments.length + uploading >= MAX_ATTACHMENTS) {
        setAttachNote(t('talk.attach.tooMany'))
        return
      }
      if (f.type.startsWith('image/')) {
        try {
          const { data, mime, bytes } = await compressImage(f)
          if (bytes > MAX_IMAGE_BYTES) {
            setAttachNote(t('talk.attach.tooLarge'))
            continue
          }
          stage({ id: crypto.randomUUID(), kind: 'image', name: f.name, mime, data, size: bytes })
        } catch {
          setAttachNote(t('talk.attach.uploadFailed'))
        }
      } else {
        setUploading(n => n + 1)
        try {
          const res = await uploadFile(f)
          stage({ id: crypto.randomUUID(), kind: 'file', name: res.name, path: res.path, size: res.size })
        } catch (e) {
          setAttachNote((e as Error)?.message || t('talk.attach.uploadFailed'))
        } finally {
          setUploading(n => n - 1)
        }
      }
    }
  }

  const removeAttachment = (id: string) => {
    onAttachmentsChange(attachments.filter(a => a.id !== id))
    setAttachNote('')
  }

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const val = e.target.value
    onInputChange(val)

    // Detect if we're typing an @mention. Worker ids may contain `-`, `.`, `_`
    // (the same charset used when declaring workers), so the token is /[\w.-]/,
    // not just /\w/.
    const cursorPos = e.target.selectionStart
    const beforeCursor = val.slice(0, cursorPos)
    const atMatch = beforeCursor.match(/@([\w.-]*)$/)
    if (atMatch) {
      setPickerOpen(true)
      setPickerMode('mention')
      setMentionQuery(atMatch[1].toLowerCase())
      setMentionIndex(0)
    } else {
      setPickerOpen(false)
    }
  }

  const selectMention = (id: string) => {
    const cursorPos = textareaRef.current?.selectionStart ?? input.length
    const beforeCursor = input.slice(0, cursorPos)
    const afterCursor = input.slice(cursorPos)
    const atMatch = beforeCursor.match(/^(.*)@[\w.-]*$/)
    if (atMatch) {
      const newVal = atMatch[1] + '@' + id + ' ' + afterCursor
      onInputChange(newVal)
      // Move cursor after the inserted mention
      requestAnimationFrame(() => {
        const ta = textareaRef.current
        if (ta) {
          const pos = atMatch[1].length + id.length + 2
          ta.setSelectionRange(pos, pos)
          ta.focus()
        }
      })
    }
  }

  // Commit a picker selection: in mention mode insert @id into the input, in
  // target mode set the persistent target. Either way close the picker.
  const commitPicker = (id: string) => {
    if (pickerMode === 'mention') selectMention(id)
    else onSelectTarget(id)
    setPickerOpen(false)
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // IME composition: keys (esp. Enter to confirm pinyin) must not trigger
    // send or mention navigation while composing.
    const composing = e.nativeEvent.isComposing || e.keyCode === 229

    // Backspace/Delete on an empty input clears the persisted @ target.
    if (!composing && input === '' && (e.key === 'Backspace' || e.key === 'Delete') && mentionTarget) {
      e.preventDefault()
      onClearMentionTarget()
      return
    }

    if (pickerOpen && !composing) {
      // Use the same pickable set the dropdown renders (archived/suspended reason
      // workers excluded, group headers inert), so Enter commits exactly the
      // highlighted option.
      const list = pickerGroup.workers
      // Arrow/ctrl+n/p cycle through the options (wrapping at both ends).
      if (list.length > 0) {
        const n = list.length
        if ((e.ctrlKey && e.key.toLowerCase() === 'n') || e.key === 'ArrowDown') {
          e.preventDefault()
          setMentionIndex(i => (i + 1) % n)
          return
        }
        if ((e.ctrlKey && e.key.toLowerCase() === 'p') || e.key === 'ArrowUp') {
          e.preventDefault()
          setMentionIndex(i => (i - 1 + n) % n)
          return
        }
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        if (list.length > 0) {
          e.preventDefault()
          commitPicker(list[mentionIndex].id)
          return
        }
      }
      if (e.key === 'Escape') {
        setPickerOpen(false)
        return
      }
    }
    if (e.key === 'Enter' && !e.shiftKey && !composing) {
      e.preventDefault()
      onSend()
    }
  }

  // Parse current @mention target for display. Matches an @mention at any
  // position (not only at the start), showing the most recent one. Worker ids
  // may contain `-`, `.`, `_`, so the token is /[\w.-]/.
  const currentTarget = useMemo(() => {
    const matches = [...input.matchAll(/@([\w.-]+)/g)]
    if (matches.length === 0) return null
    const last = matches[matches.length - 1]
    const w = reasonWorkers.find(r => r.id === last[1])
    return w ? w.id : null
  }, [input, reasonWorkers])

  // Close the picker on a click outside.
  useEffect(() => {
    const handler = () => { setPickerOpen(false); setModeOpen(false) }
    window.addEventListener('click', handler)
    return () => window.removeEventListener('click', handler)
  }, [])

  // Focus the textarea when a mention is triggered from outside
  useEffect(() => {
    if (mentionKey > 0) {
      textareaRef.current?.focus()
    }
  }, [mentionKey])

  // Archived or suspended reason workers are not mentionable / targetable.
  const pickableWorkers = reasonWorkers.filter(w => !archived.has(w.id) && w.state !== 'suspended')
  // Mention mode narrows by the typed query; target mode shows everything.
  const pickableShown = (pickerMode === 'mention')
    ? pickableWorkers.filter(w => w.id.toLowerCase().includes(mentionQuery))
    : pickableWorkers

  // Build the picker's grouped option list. Reason workers are grouped by
  // their primary (first) tag's top-level path segment — a slash tag nests
  // visually via indentation, so "ops/backup" and "ops/analytics" sit under
  // an "ops" header at the right depth. Workers without tags fall into a
  // trailing "other" group. Group headers are inert; they never consume the
  // keyboard-active slot, so navigation stays on the worker rows only.
  const pickerGroup = useMemo(() => {
    const opts: PickerOption[] = []
    const workers: WorkerInfo[] = []
    let lastGroup: string | null = null
    const emitGroup = (g: string) => {
      if (g !== lastGroup) {
        opts.push({ id: '__grp_' + g, label: g, isGroup: true })
        lastGroup = g
      }
    }
    const tagged = pickableShown.filter(w => (w.tags?.length ?? 0) > 0)
      .slice().sort((a, b) => a.tags![0].localeCompare(b.tags![0]))
    const untagged = pickableShown.filter(w => !w.tags?.length)
    for (const w of tagged) {
      const primary = w.tags![0]
      const segs = primary.split('/')
      emitGroup(segs[0])
      const indent = Math.min(segs.length - 1, 3)
      const secondary = w.description || primary
      opts.push({
        id: w.id,
        label: w.id,
        sublabel: w.type,
        indent,
        hint: secondary,
        description: secondary,
      })
      workers.push(w)
    }
    if (untagged.length) {
      emitGroup(t('picker.group.untagged'))
      for (const w of untagged) {
        opts.push({ id: w.id, label: w.id, sublabel: w.type, description: w.description })
        workers.push(w)
      }
    }
    // The highlighted worker row's index within the option list (group
    // headers don't count for keyboard navigation).
    let activeOptionIndex = -1
    if (workers[mentionIndex]) {
      activeOptionIndex = opts.findIndex(o => !o.isGroup && o.id === workers[mentionIndex].id)
    }
    return { opts, workers, activeOptionIndex }
  }, [pickableShown, mentionIndex, t])
  const pickList = pickerGroup.opts

  // Persistent target: a specific reason worker, or '' (broadcast).
  const persistentTarget = mentionTarget && reasonWorkers.some(r => r.id === mentionTarget) ? mentionTarget : ''
  // The single visible target: an immediate @ in the input takes priority, then
  // the persistent target, otherwise broadcast.
  const shownTarget = currentTarget || persistentTarget

  // Input-mode selector options. Descriptions condensed from pkg/reason:
  //   schedule  = level 1 (only when idle; least intrusive)
  //   append    = level 2 (no interrupt; respond promptly next round)
  //   interrupt = level 3 (cancel in-flight reasoning, handle now)
  const modeOptions: PickerOption[] = [
    { id: 'default', label: t('mode.interrupt'), description: t('mode.interrupt.desc'), hint: t('mode.interrupt.hint') },
    { id: 'append', label: t('mode.append'), description: t('mode.append.desc'), hint: t('mode.append.hint') },
    { id: 'schedule', label: t('mode.schedule'), description: t('mode.schedule.desc'), hint: t('mode.schedule.hint') },
  ]
  const currentModeLabel = modeOptions.find(m => m.id === inputMode)?.label ?? inputMode
  // Compact labels for the mobile action row: '模式: 打断模式' does not fit.
  const modeShortLabel: Record<string, string> = {
    default: t('mode.interrupt.short'),
    append: t('mode.append.short'),
    schedule: t('mode.schedule.short'),
  }

  // The mode dropdown is 420px wide on desktop; on phones it must fit the
  // viewport (minus padding) or it overflows the screen.
  const modePickerWidth = Math.min(420, Math.max(280, (typeof window !== 'undefined' ? window.innerWidth : 420) - 24))

  const targetChip = (
    <div style={{ position: 'relative', display: 'flex', ...(isMobile ? { width: '100%' } : { marginRight: 'auto' }) }}>
        <span
          onClick={(e) => { e.stopPropagation(); setPickerMode('target'); setMentionIndex(0); setPickerOpen(v => !v) }}
          title={shownTarget ? t('talk.input.target.tooltip', { target: shownTarget }) : t('talk.input.broadcast.tooltip')}
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 13, lineHeight: '20px',
            color: shownTarget ? colors.accent : colors.textDimmed,
            border: '1px solid ' + (shownTarget ? colors.accentBorder : colors.border),
            borderRadius: 2, padding: '4px 10px', cursor: 'pointer', userSelect: 'none',
          }}
        >
          {shownTarget ? `→ ${shownTarget}` : t('talk.input.broadcast')}
        </span>

        {pickerOpen && (
          <div style={{ position: 'absolute', left: 0, bottom: '100%', marginBottom: 4, zIndex: 100 }}>
            <PickerDropdown
              header={pickerMode === 'mention' ? t('picker.mention') : t('picker.target')}
              options={pickList}
              selectedId={pickerMode === 'target' ? (shownTarget || undefined) : undefined}
              activeIndex={pickerGroup.activeOptionIndex}
              onSelect={commitPicker}
              onActivate={(i) => {
                // Map the option index back to the worker it selects: group
                // headers are skipped (they are inert structure).
                setMentionIndex(pickerGroup.opts.slice(0, i + 1).filter(o => !o.isGroup).length - 1)
              }}
              footer={{
                label: t('picker.broadcast'),
                checked: !shownTarget,
                onChoose: () => {
                  if (pickerMode === 'mention') {
                    // Cancelling an in-progress @mention: strip the trailing
                    // "@word" so nothing is left in the text. Same charset as
                    // the other mention parsers: /[\w.-]/.
                    onInputChange(input.replace(/@[\w.-]*$/, ''))
                  } else {
                    onClearMentionTarget()
                  }
                  setPickerOpen(false)
                },
              }}
            />
          </div>
        )}
    </div>
  )

  return (
    <div style={{ padding: '12px 24px', borderTop: '1px solid ' + colors.border, position: 'relative' }}>

      {/* Attachment chips: staged images (thumbnail) and uploaded files
          (name), each removable, plus upload/note status. */}
      {(attachments.length > 0 || uploading > 0 || attachNote) && (
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', margin: '4px 0' }}>
          {attachments.map(a => (
            <span
              key={a.id}
              title={a.kind === 'file' ? a.path : `${a.name} (${Math.ceil(a.size / 1024)}KB)`}
              style={{
                display: 'inline-flex', alignItems: 'center', gap: 6, padding: '3px 8px',
                border: '1px solid ' + colors.border, borderRadius: 4, background: colors.bgLight,
                fontSize: fontSizes.sm, color: colors.textDim, userSelect: 'none',
              }}
            >
              {a.kind === 'image' ? (
                <img src={`data:${a.mime};base64,${a.data}`} alt={a.name} style={{ width: 28, height: 28, objectFit: 'cover', borderRadius: 2, display: 'block' }} />
              ) : (
                <span>{'📄'}</span>
              )}
              <span style={{ maxWidth: 160, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{a.name}</span>
              <span
                onClick={() => removeAttachment(a.id)}
                className="btn-hover"
                style={{ cursor: 'pointer', color: colors.textDimmed, padding: '0 2px', userSelect: 'none' }}
              >
                {'\u2715'}
              </span>
            </span>
          ))}
          {uploading > 0 && (
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, color: colors.textDimmed, fontSize: fontSizes.sm }}>
              <span className="niq-spinner" style={{ width: 12, height: 12, borderWidth: 2, borderColor: colors.accent, borderTopColor: 'transparent' }} />
              {t('talk.attach.uploading')}
            </span>
          )}
          {attachNote && <span style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{attachNote}</span>}
        </div>
      )}

      {/* Mobile: the target/mention chip is its own full-width row above the
          input; on desktop it lives in the action row below. */}
      {isMobile && (
        <div style={{ marginBottom: 6 }}>
          {targetChip}
        </div>
      )}
      <textarea
        ref={textareaRef}
        value={input}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        onPaste={(e) => {
          // Pasted images stage as attachments instead of leaking into the
          // text as a path or binary junk.
          if (e.clipboardData?.files?.length) {
            e.preventDefault()
            addFiles(e.clipboardData.files)
          }
        }}
        className="talk-input"
        placeholder={t('talk.input.placeholder')}
        rows={3}
        style={{
          width: '100%',
          background: 'transparent',
          color: colors.text,
          border: 'none',
          outline: 'none',
          padding: '8px 0',
          fontSize: 14,
          resize: 'none',
          lineHeight: 1.5,
          boxSizing: 'border-box',
        }}
      />
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: isMobile ? 6 : 12, rowGap: 6, justifyContent: 'flex-end', alignItems: 'center' }}>
      {!isMobile && targetChip}
        {/* File picker: non-image files upload to the project's upload dir
            and enter the input as path references. */}
        <input
          ref={fileInputRef}
          type="file"
          multiple
          style={{ display: 'none' }}
          onChange={(e) => { if (e.target.files?.length) addFiles(e.target.files); e.target.value = '' }}
        />
        {/* Attach: text button at the left edge of the right-aligned
            action cluster (mode / send / stop). */}
        <span
          onClick={() => fileInputRef.current?.click()}
          title={t('talk.attach')}
          style={{
            cursor: 'pointer', userSelect: 'none',
            color: colors.textDim, fontSize: 13, lineHeight: '20px',
            padding: '4px 10px',
            border: '1px solid ' + colors.border, borderRadius: 4,
          }}
        >
          {t('talk.attach.add')}
        </span>
        <div style={{ position: 'relative', display: 'inline-block' }}>
          <span
            onClick={(e) => { e.stopPropagation(); setModeOpen(v => !v) }}
            title={modeOptions.find(m => m.id === inputMode)?.hint}
            style={{
              display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 13, lineHeight: '20px',
              color: colors.textDim,
              border: '1px solid ' + colors.border, borderRadius: 2, padding: '4px 10px',
              cursor: 'pointer', userSelect: 'none',
            }}
          >
            {isMobile ? (modeShortLabel[inputMode] ?? currentModeLabel) : `${t('talk.input.mode')}: ${currentModeLabel}`}
            <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>▾</span>
          </span>
          {modeOpen && (
            <div style={{ position: 'absolute', right: 0, bottom: '100%', marginBottom: 4, zIndex: 100 }}>
              <PickerDropdown
                header={t('picker.header.mode')}
                options={modeOptions}
                selectedId={inputMode}
                onSelect={(id) => { onModeChange(id); setModeOpen(false) }}
                width={modePickerWidth}
              />
            </div>
          )}
        </div>
        <button
          onClick={onSend}
          className="btn-send"
          style={{
            background: 'none',
            color: colors.text,
            border: '1px solid ' + colors.border,
            padding: '4px 10px',
            borderRadius: 4,
            cursor: 'pointer',
            fontSize: 13,
            lineHeight: '20px',
          }}
        >
          {t('talk.input.send')}
        </button>
        <button
          onClick={onAbort}
          className="btn-stop"
          style={{
            background: 'none',
            color: colors.textDim,
            border: '1px solid ' + colors.border,
            padding: '4px 10px',
            borderRadius: 4,
            cursor: 'pointer',
            fontSize: isMobile ? 15 : 13,
            lineHeight: '20px',
          }}
        >
          {t('talk.input.stop')}
        </button>
      </div>
    </div>
  )
}