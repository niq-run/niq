import { useState, useRef, useEffect, useMemo, useCallback } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import PickerDropdown, { type PickerOption } from './PickerDropdown'
import { uploadFile } from '../services/api'
import { isTalkPartnerType, type StagedAttachment, type WorkerInfo } from '../types'
import { mentionsIn, leadingMentions } from './talk-utils'

// Attachment limits: images ride inline as base64, files go through
// /api/upload and only their path enters the input.
const MAX_ATTACHMENTS = 4
const MAX_IMAGE_BYTES = 5 * 1024 * 1024
// Images above this edge length are downscaled before embedding — the
// useful resolution ceiling of mainstream vision models, and a large win
// for event/snapshot size.
const IMAGE_MAX_DIM = 1568
const IMAGE_JPEG_QUALITY = 0.85

// Recursively read a directory entry into a flat list of File objects. A
// dropped/pasted folder shows up as a directory entry, not a File, so we walk
// it with the File System API (webkitGetAsEntry) and collect every file inside
// (preserving each file's own name; nested paths are flattened — the upload
// endpoint stores files flat in the project's upload dir anyway).
async function filesFromEntry(entry: FileSystemEntry, out: File[] = []): Promise<File[]> {
  if (entry.isFile) {
    const fe = entry as FileSystemFileEntry
    try {
      const f = await new Promise<File>((res, rej) => fe.file(res, rej))
      out.push(f)
    } catch { /* unreadable file: skip */ }
    return out
  }
  if (entry.isDirectory) {
    const reader = (entry as FileSystemDirectoryEntry).createReader()
    // readEntries returns in batches; loop until it reports none left.
    for (;;) {
      const batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej))
      if (batch.length === 0) break
      for (const child of batch) await filesFromEntry(child, out)
    }
  }
  return out
}

// Collect every File from a drop or paste payload, expanding folder items via
// their directory entries. Falls back to the plain file list when the modern
// entry API is unavailable.
async function filesFromTransfer(items: DataTransferItemList | undefined, files: FileList | null): Promise<File[]> {
  const out: File[] = []
  if (items && items.length) {
    for (const item of Array.from(items)) {
      if (item.kind !== 'file') continue
      if (typeof (item as any).webkitGetAsEntry === 'function') {
        const entry = (item as any).webkitGetAsEntry() as FileSystemEntry | null
        if (entry) { await filesFromEntry(entry, out); continue }
      }
      const f = item.getAsFile()
      if (f) out.push(f)
    }
    return out
  }
  return files ? Array.from(files) : out
}

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
  isMobile: boolean
  // Attachments staged for the next send: pasted/picked images (inline
  // base64) and uploaded files (referenced by path).
  attachments: StagedAttachment[]
  // Accepts either a full list or an updater fn (the parent's setAttachments)
  // so TalkInput can append atomically without a stale-closure read of the
  // current list.
  onAttachmentsChange: (a: StagedAttachment[] | ((prev: StagedAttachment[]) => StagedAttachment[])) => void
  // Called when the input box gains focus — App trims the list so typing is
  // crisp even if it had grown large while following.
  onFocus?: () => void
}

export default function TalkInput({ talkPartner, input, inputMode, onInputChange, onSend, onAbort, onModeChange, workers, archived, mentionKey, isMobile, attachments, onAttachmentsChange, onFocus }: TalkInputProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  // The @mention picker is the only picker the input opens: it addresses the
  // message to one or more workers. There is no "target"/broadcast mode — a
  // send always needs at least one recipient.
  const [pickerOpen, setPickerOpen] = useState(false)
  // Live query for the worker picker's search box (seeded from the @token being
  // typed); used to filter the dropdown.
  const [pickerQuery, setPickerQuery] = useState('')
  const [mentionIndex, setMentionIndex] = useState(0)
  const [modeOpen, setModeOpen] = useState(false)
  const [uploading, setUploading] = useState(0)
  const [attachNote, setAttachNote] = useState('')
  // Drag-and-drop: tracks whether a file is being dragged over the input so
  // the drop target is visibly highlighted. dragDepth counts enter/leave to
  // survive drag events bubbling over child elements.
  const [dragging, setDragging] = useState(false)
  const dragDepth = useRef(0)
  // Running staged-attachment count (attachments.length is the prop, which
  // is stale inside an async addFiles loop — a folder drop must cap against
  // what's already committed, not the value at render time).
  const stagedCountRef = useRef(attachments.length)
  // Align the running count whenever the committed list moves independently
  // of stage/removeAttachment — e.g. a send clears attachments directly in
  // the parent.
  const lastPropCount = useRef(attachments.length)
  if (lastPropCount.current !== attachments.length) {
    lastPropCount.current = attachments.length
    stagedCountRef.current = attachments.length
  }

  // Talk partners: workers that consume worker.input — reason, and niw /
  // remote-niw bridges. These are the mentionable / targetable workers in the
  // input.
  const partners = useMemo(() => workers.filter(w => isTalkPartnerType(w.type)), [workers])

  // ── Attachments ──
  const stage = (a: StagedAttachment) => {
    // Functional update: addFiles stages multiple files in one async pass,
    // and each call must append to the latest list — not the `attachments`
    // prop captured when this render's closure was created (which would
    // clobber earlier appends and show only the last file).
    onAttachmentsChange(prev => [...prev, a])
    stagedCountRef.current += 1
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
      if (stagedCountRef.current >= MAX_ATTACHMENTS) {
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
    onAttachmentsChange(prev => prev.filter(a => a.id !== id))
    setAttachNote('')
    stagedCountRef.current = Math.max(0, stagedCountRef.current - 1)
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
      setPickerQuery(atMatch[1].toLowerCase())
      setMentionIndex(0)
    } else {
      setPickerOpen(false)
    }
  }

  // mentioned is the @-addressed workers present in the input, in
  // first-appearance order. They are the message's recipients, so the chip can
  // show exactly who will receive it and the picker can check them off.
  const isPartnerId = useCallback((id: string) => partners.some(w => w.id === id), [partners])
  const mentioned = useMemo(() => mentionsIn(input, isPartnerId), [input, isPartnerId])

  // A send with nobody addressed is refused, and saying why needs somewhere
  // visible: the Send button can carry a hover tooltip, but Enter has no hover,
  // so the refusal also flashes a strip next to the recipient chip.
  const [noTargetHint, setNoTargetHint] = useState(false)
  const noTargetTimer = useRef<number | undefined>(undefined)
  useEffect(() => () => window.clearTimeout(noTargetTimer.current), [])
  const flashNoTarget = useCallback(() => {
    setNoTargetHint(true)
    window.clearTimeout(noTargetTimer.current)
    noTargetTimer.current = window.setTimeout(() => setNoTargetHint(false), 2600)
  }, [])
  // Adding an addressee answers the hint, so it goes away at once.
  useEffect(() => {
    if (mentioned.length > 0) setNoTargetHint(false)
  }, [mentioned.length])

  // insertMention puts "@id " at the caret. A trailing "@query" being typed is
  // replaced by the pick; otherwise (chip pick on an already-complete token) it
  // simply inserts at the caret, so picks accumulate into a multi-mention.
  const insertMention = (id: string) => {
    const cursorPos = textareaRef.current?.selectionStart ?? input.length
    const beforeCursor = input.slice(0, cursorPos)
    const afterCursor = input.slice(cursorPos)
    const atMatch = beforeCursor.match(/^(.*)@[\w.-]*$/)
    const head = atMatch ? atMatch[1] : beforeCursor
    // Keep one space between existing text and the inserted mention.
    const sep = head && !/\s$/.test(head) ? ' ' : ''
    const prefix = head + sep + '@' + id + ' '
    onInputChange(prefix + afterCursor)
    requestAnimationFrame(() => {
      const ta = textareaRef.current
      if (ta) {
        ta.setSelectionRange(prefix.length, prefix.length)
        ta.focus()
      }
    })
  }

  // removeMention takes one addressee back out (the picker row toggles). The id
  // charset is [\w.-], so `.` is the only regex-special character to escape.
  const removeMention = (id: string) => {
    const token = id.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
    const next = input.replace(new RegExp('(^|\\s)@' + token + '(?![\\w.-])\\s?', 'g'), '$1')
    onInputChange(next.trimStart())
  }

  // Commit a picker selection. Enter commits without toggling (choosing a row
  // should never take a mention away); a click toggles, so the picker doubles
  // as a multi-select editor.
  const commitPicker = (id: string, toggle = true) => {
    if (toggle && mentioned.includes(id)) removeMention(id)
    else if (!mentioned.includes(id)) insertMention(id)
    setPickerOpen(false)
    setPickerQuery('')
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // IME composition: keys (esp. Enter to confirm pinyin) must not trigger
    // send or mention navigation while composing.
    const composing = e.nativeEvent.isComposing || e.keyCode === 229

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
          // Commit the highlighted row, clamped into the list: the options
          // narrow as the query grows, and a highlight left over from a longer
          // list must still land on a real row (that is why Enter used to look
          // like it did nothing).
          const idx = Math.min(Math.max(mentionIndex, 0), list.length - 1)
          commitPicker(list[idx].id, false)
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
      // Nothing addressed means nothing to send: say so instead of swallowing
      // the keystroke. (An addressed but empty draft is just a no-op.)
      if (mentioned.length === 0) {
        flashNoTarget()
        return
      }
      if (!canSend) return
      onSend()
    }
  }

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
  const pickableWorkers = partners.filter(w => !archived.has(w.id) && w.state !== 'suspended')
  // The dropdown's search box filters by worker id across both modes; an empty
  // query keeps everything.
  const pickableShown = pickableWorkers.filter(w =>
    pickerQuery.trim() === '' || w.id.toLowerCase().includes(pickerQuery.trim().toLowerCase()))

  // Build the picker's option list. While searching it is one flat list with
  // the best hit first: an exact id match outranks a prefix hit, which outranks
  // a substring hit, so spelling a worker's name puts it on top — where the
  // default highlight (and Enter) lands. With no query, reason workers are
  // grouped by their primary (first) tag's top-level path segment — a slash tag
  // nests visually via indentation, so "ops/backup" and "ops/analytics" sit
  // under an "ops" header at the right depth — and workers without tags fall
  // into a trailing "other" group. Group headers are inert; they never consume
  // the keyboard-active slot, so navigation stays on the worker rows only.
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
    const query = pickerQuery.trim().toLowerCase()
    if (query) {
      const rank = (w: WorkerInfo) => {
        const id = w.id.toLowerCase()
        return id === query ? 0 : id.startsWith(query) ? 1 : 2
      }
      // Stable ranking: equal ranks keep the workers' natural order, so the list
      // does not shuffle while the query grows.
      pickableShown
        .map((w, i) => ({ w, i }))
        .sort((a, b) => rank(a.w) - rank(b.w) || a.i - b.i)
        .forEach(({ w }) => {
          const secondary = w.description || w.tags?.[0]
          opts.push({ id: w.id, label: w.id, sublabel: w.type, hint: secondary, description: secondary })
          workers.push(w)
        })
    } else {
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
    }
    // The highlighted worker row's index within the option list (group
    // headers don't count for keyboard navigation). The index is clamped into
    // the list: it can go stale when the query narrows the options, and the
    // highlight must stay on a real row — the same row Enter would take.
    let activeOptionIndex = -1
    if (workers.length > 0) {
      const idx = Math.min(Math.max(mentionIndex, 0), workers.length - 1)
      activeOptionIndex = opts.findIndex(o => !o.isGroup && o.id === workers[idx].id)
    }
    return { opts, workers, activeOptionIndex }
  }, [pickableShown, pickerQuery, mentionIndex, t])
  const pickList = pickerGroup.opts

  // A message needs at least one addressee: the @mentions are the recipients
  // and there is no broadcast path any more. It also needs some content — the
  // sidebar prefills the addressing prefix, so a bare "@worker " is an empty
  // draft, not a message.
  const body = leadingMentions(input).rest.trim()
  const canSend = mentioned.length > 0 && (body !== '' || attachments.length > 0)
  // Chip label: the addressees, or a prompt to pick one.
  const targetsLabel = mentioned.length > 0
    ? mentioned.map(id => '@' + id).join(', ')
    : t('talk.input.noTarget')

  // Input-mode selector options. Descriptions condensed from pkg/reason:
  //   schedule  = level 1 (only when idle; least intrusive)
  //   append    = level 2 (no interrupt; respond promptly next round)
  //   interrupt = level 3 (cancel in-flight reasoning, handle now)
  const modeOptions: PickerOption[] = [
    { id: 'interrupt', label: t('mode.interrupt'), description: t('mode.interrupt.desc'), hint: t('mode.interrupt.hint') },
    { id: 'append', label: t('mode.append'), description: t('mode.append.desc'), hint: t('mode.append.hint') },
    { id: 'schedule', label: t('mode.schedule'), description: t('mode.schedule.desc'), hint: t('mode.schedule.hint') },
  ]
  const currentModeLabel = modeOptions.find(m => m.id === inputMode)?.label ?? inputMode
  // One size for the whole action cluster (attach/stop/mode/send) so they read
  // as one row; slightly larger on phones for a comfortable tap target.
  const actionFont = isMobile ? 15 : 13

  // The mode dropdown is 420px wide on desktop; on phones it must fit the
  // viewport (minus padding) or it overflows the screen.
  const modePickerWidth = Math.min(420, Math.max(280, (typeof window !== 'undefined' ? window.innerWidth : 420) - 24))

  const targetChip = (
    <div style={{ position: 'relative', display: 'flex', minWidth: 0, ...(isMobile ? { flexShrink: 1 } : { marginRight: 'auto' }) }}>
        <span
          onClick={(e) => { e.stopPropagation(); setPickerQuery(''); setMentionIndex(0); setPickerOpen(v => !v) }}
          title={mentioned.length > 0
            ? t('talk.input.targets.tooltip', { targets: targetsLabel })
            : t('talk.input.noTarget.tooltip')}
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: actionFont, lineHeight: '20px',
            color: mentioned.length > 0 ? colors.accent : colors.textDimmed,
            border: '1px solid ' + (mentioned.length > 0 ? colors.accentBorder : colors.border),
            borderRadius: 2, padding: '4px 10px', cursor: 'pointer', userSelect: 'none',
          }}
        >
          {/* Wrap the label so long / multiple worker ids truncate instead of
              widening the chip past the mobile button row. */}
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: isMobile ? '14ch' : undefined }}>
            {targetsLabel}
          </span>
        </span>

        {/* Refusal strip, glued to the chip's right edge: the chip itself says
            "no worker", this says what to do about it. A muted amber (the
            palette's warn tone) reads as attention, not as an error. */}
        {noTargetHint && (
          <span
            style={{
              display: 'inline-flex', alignItems: 'center',
              marginLeft: 6, minWidth: 0, flexShrink: 1,
              fontSize: actionFont, lineHeight: '20px',
              color: colors.warn,
              border: '1px solid ' + colors.warn, borderRadius: 2,
              padding: '4px 10px', userSelect: 'none',
            }}
          >
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {t('talk.input.noTarget.hint')}
            </span>
          </span>
        )}

        {pickerOpen && (
          <div style={{ position: 'absolute', left: 0, bottom: '100%', marginBottom: 4, zIndex: 100 }}>
            <PickerDropdown
              header={t('picker.mention')}
              options={pickList}
              selectedIds={mentioned}
              activeIndex={pickerGroup.activeOptionIndex}
              onSelect={commitPicker}
              width={isMobile ? 480 : 240}
              onActivate={(i) => {
                // Map the option index back to the worker it selects: group
                // headers are skipped (they are inert structure).
                setMentionIndex(pickerGroup.opts.slice(0, i + 1).filter(o => !o.isGroup).length - 1)
              }}
              searchValue={pickerQuery}
              onSearchValueChange={setPickerQuery}
              searchPlaceholder={t('workerPicker.search')}
            />
          </div>
        )}
    </div>
  )

  return (
    <div
      onDragEnter={(e) => {
        if (!e.dataTransfer?.types?.includes('Files')) return
        e.preventDefault()
        dragDepth.current += 1
        setDragging(true)
      }}
      onDragOver={(e) => {
        if (!e.dataTransfer?.types?.includes('Files')) return
        e.preventDefault()
      }}
      onDragLeave={(e) => {
        dragDepth.current = Math.max(0, dragDepth.current - 1)
        if (dragDepth.current === 0) setDragging(false)
      }}
      onDrop={async (e) => {
        e.preventDefault()
        const files = await filesFromTransfer(e.dataTransfer?.items, e.dataTransfer?.files ?? null)
        if (files.length) addFiles(files)
        dragDepth.current = 0
        setDragging(false)
      }}
      style={{ padding: '12px 24px', borderTop: '1px solid ' + colors.border, position: 'relative', paddingBottom: 'calc(12px + env(safe-area-inset-bottom, 0px))', ...(dragging ? { outline: '2px dashed ' + colors.accent, outlineOffset: -2 } : {}) }}
    >
      {/* Drag overlay: a full-area hint shown while files hover over the
          input, so dropping is discoverable. Sits above the text. */}
      {dragging && (
        <div style={{ position: 'absolute', inset: 0, zIndex: 10, display: 'flex', alignItems: 'center', justifyContent: 'center', background: colors.bgChip, pointerEvents: 'none' }}>
          <span style={{ color: colors.accent, fontSize: fontSizes.md, fontWeight: 600 }}>{t('talk.attach.drop')}</span>
        </div>
      )}

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

      <textarea
        ref={textareaRef}
        value={input}
        onChange={handleChange}
        onFocus={onFocus}
        onKeyDown={handleKeyDown}
        onPaste={async (e) => {
          // Pasted images / files stage as attachments instead of leaking into
          // the text as a path or binary junk. A pasted folder is expanded via
          // its directory entry so its files upload too.
          const files = await filesFromTransfer(e.clipboardData?.items, e.clipboardData?.files ?? null)
          if (files.length) {
            e.preventDefault()
            addFiles(files)
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
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: isMobile ? 4 : 12, rowGap: isMobile ? 4 : 6, justifyContent: 'flex-end', alignItems: 'center' }}>
        {targetChip}
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
            action cluster (stop / mode / send). */}
        <span
          onClick={() => fileInputRef.current?.click()}
          title={t('talk.attach')}
          style={{
            cursor: 'pointer', userSelect: 'none',
            color: colors.textDim, fontSize: actionFont, lineHeight: '20px',
            padding: '4px 10px',
            border: '1px solid ' + colors.border, borderRadius: 4,
          }}
        >
          {t('talk.attach.add')}
        </span>
        {/* Stop sits left of the mode selector, so the mode label — and the
            dropdown anchored to it — stays next to Send on the right. */}
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
            fontSize: actionFont,
            lineHeight: '20px',
          }}
        >
          {t('talk.input.stop')}
        </button>
        <div style={{ position: 'relative', display: 'inline-block' }}>
          <span
            onClick={(e) => { e.stopPropagation(); setModeOpen(v => !v) }}
            title={modeOptions.find(m => m.id === inputMode)?.hint}
            style={{
              display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: actionFont, lineHeight: '20px',
              color: colors.textDim,
              border: '1px solid ' + colors.border, borderRadius: 2, padding: '4px 10px',
              cursor: 'pointer', userSelect: 'none',
            }}
          >
            {isMobile ? t('talk.input.mode') : `${t('talk.input.mode')}: ${currentModeLabel}`}
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
        {/* Send needs a recipient and some content. It stays clickable while
            unready — pressing it explains what is missing (a dead button would
            silently swallow the press), and the tooltip says so on hover. */}
        <button
          onClick={() => {
            if (mentioned.length === 0) { flashNoTarget(); return }
            if (!canSend) return
            onSend()
          }}
          aria-disabled={!canSend}
          title={canSend ? undefined : mentioned.length === 0 ? t('talk.input.send.blocked') : undefined}
          className={canSend ? 'btn-send' : undefined}
          style={{
            background: 'none',
            color: canSend ? colors.text : colors.textDimmed,
            border: '1px solid ' + colors.border,
            padding: '4px 10px',
            borderRadius: 4,
            cursor: canSend ? 'pointer' : 'default',
            fontSize: actionFont,
            lineHeight: '20px',
          }}
        >
          {t('talk.input.send')}
        </button>
      </div>
    </div>
  )
}