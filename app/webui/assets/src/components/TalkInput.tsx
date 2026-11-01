import { useState, useRef, useEffect, useMemo, useCallback } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import PickerDropdown, { type PickerOption } from './PickerDropdown'
import WorkerPickerModal from './WorkerPickerModal'
import { uploadFile } from '../services/api'
import { isTalkPartnerType, type StagedAttachment, type WorkerInfo } from '../types'
import { mentionsIn, leadingMentions, mergeTargets, mentionName } from './talk-utils'

// Attachment limits: images ride inline as base64, files go through
// /api/upload and only their path enters the input.
const MAX_ATTACHMENTS = 4
const MAX_IMAGE_BYTES = 5 * 1024 * 1024
// Images above this edge length are downscaled before embedding — the
// useful resolution ceiling of mainstream vision models, and a large win
// for event/snapshot size.
const IMAGE_MAX_DIM = 1568
const IMAGE_JPEG_QUALITY = 0.85

// ── Input box size ──
// Where the user's chosen input-box height is kept, so it survives reloads
// and the conversation switches that remount the talk view's input.
const BOX_HEIGHT_KEY = 'niq-talk-input-height'
// The input box's size: the height the user settled on (null = the
// textarea's natural 3-row size) and whether the tall expand preset is on.
interface BoxState {
  h: number | null
  expanded: boolean
}
// The shortest the box may get — about two lines of text.
const MIN_BOX_H = 56
// The room the layout needs besides the input box: the page header, the
// panel's own chrome (grip strip, action row, padding) and a usable slice
// of the conversation above.
const RESERVED_H = 240
// The tallest the box may get, so the conversation never disappears.
function maxBoxHeight(): number {
  const h = typeof window !== 'undefined' ? window.innerHeight : 800
  return Math.max(MIN_BOX_H, h - RESERVED_H)
}
// The expand preset: a fairly tall box, but never more than the room the
// layout can spare.
function expandedBoxHeight(): number {
  const h = typeof window !== 'undefined' ? window.innerHeight : 800
  return Math.round(Math.min(maxBoxHeight(), h * 0.45))
}

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
  // The message's target set (its own state, owned by App): the worker selector
  // seeds it, the recipient picker edits it. Never written into the draft.
  targets: string[]
  onTargetsChange: (next: string[]) => void
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

export default function TalkInput({ talkPartner, input, inputMode, onInputChange, onSend, onAbort, onModeChange, workers, archived, mentionKey, targets, onTargetsChange, isMobile, attachments, onAttachmentsChange, onFocus }: TalkInputProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  // The @mention picker is the only picker the input opens: it addresses the
  // message to one or more workers. There is no "target"/broadcast mode — a
  // send always needs at least one recipient.
  // Which list the open picker edits:
  //   'text'   — the @token being typed; a pick rewrites the draft (that is how
  //              a typed mention is completed).
  //   'target' — opened from the recipient chip; a pick edits the recipient set
  //              (the same set the sidebar checkboxes drive) and leaves the
  //              draft alone, so choosing who to address never puts text in the
  //              box.
  const [pickerFor, setPickerFor] = useState<'text' | 'target'>('text')
  const [pickerOpen, setPickerOpen] = useState(false)
  // Live query for the worker picker's search box (seeded from the @token being
  // typed); used to filter the dropdown.
  const [pickerQuery, setPickerQuery] = useState('')
  const [mentionIndex, setMentionIndex] = useState(0)
  const [modeOpen, setModeOpen] = useState(false)
  // The expanded recipient picker: the chip's dropdown is compact, so a long
  // fleet gets the same centered dialog shape as the sidebar's selector. Both
  // edit the same recipient set.
  const [targetModalOpen, setTargetModalOpen] = useState(false)
  const [uploading, setUploading] = useState(0)
  const [attachNote, setAttachNote] = useState('')
  // Drag-and-drop: tracks whether a file is being dragged over the input so
  // the drop target is visibly highlighted. dragDepth counts enter/leave to
  // survive drag events bubbling over child elements.
  const [dragging, setDragging] = useState(false)
  const dragDepth = useRef(0)
  // ── Input box height ──
  // The textarea starts at its default 3-row size (h === null). The user can
  // drag the panel's top edge to give it more room, or hit the expand button
  // to jump it to a tall preset.
  //
  // The user's adjustment is the default from then on: it is persisted, so
  // the box keeps the height they chose across reloads and across the
  // conversation switches that remount the talk view's input.
  const [boxState, setBoxState] = useState<BoxState>(() => {
    try {
      const raw = localStorage.getItem(BOX_HEIGHT_KEY)
      const s = raw ? JSON.parse(raw) as BoxState : null
      if (s && (s.h === null || (Number.isFinite(s.h) && s.h > 0))) {
        return { h: s.h, expanded: !!s.expanded }
      }
    } catch { /* ignore: unreadable or private-mode storage */ }
    return { h: null, expanded: false }
  })
  // Persist on every change but a drag: a drag writes to localStorage only
  // once, when the pointer is released.
  const saveBox = (s: BoxState, persist = true) => {
    setBoxState(s)
    if (!persist) return
    try { localStorage.setItem(BOX_HEIGHT_KEY, JSON.stringify(s)) } catch { /* ignore */ }
  }
  // The grip highlights on hover.
  const [gripHover, setGripHover] = useState(false)
  // Live drag state: the pointer position the drag started at and the box
  // height at that moment (the new height is derived from the pointer delta).
  const resizeRef = useRef<{ startY: number; startH: number; h: number } | null>(null)
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

    // Detect if we're typing an @mention. The token runs to the next space
    // rather than over the id charset: a mention may be a display name (Chinese,
    // punctuation), and only the pick's own name list can tell how much of it is
    // the name.
    const cursorPos = e.target.selectionStart
    const beforeCursor = val.slice(0, cursorPos)
    const atMatch = beforeCursor.match(/@([^\s@]*)$/)
    if (atMatch) {
      setPickerFor('text')
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
  const mentioned = useMemo(() => mentionsIn(input, partners), [input, partners])

  // Recipients: the target set plus whatever the draft @-mentions. Typing an @
  // appends to the set (de-duplicated) instead of replacing it, so the chip
  // always shows the full set the message will go to.
  const typedTargets = mentioned
  const recipients = mergeTargets(targets, typedTargets)
  // What the picker's checkmarks reflect: a typed mention being completed, or
  // the recipient set the chip edits (target set plus typed mentions).
  const checkedTargets = pickerFor === 'target' ? recipients : typedTargets

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

  // insertMention puts "@name " at the caret — the display name when the worker
  // has one, so the draft reads with the name the reader knows. A trailing
  // "@query" being typed is replaced by the pick; otherwise (chip pick on an
  // already-complete token) it simply inserts at the caret, so picks accumulate
  // into a multi-mention.
  const insertMention = (id: string) => {
    const w = partners.find(p => p.id === id)
    const name = w ? mentionName(w) : id
    const cursorPos = textareaRef.current?.selectionStart ?? input.length
    const beforeCursor = input.slice(0, cursorPos)
    const afterCursor = input.slice(cursorPos)
    const atMatch = beforeCursor.match(/^(.*)@[^\s@]*$/)
    const head = atMatch ? atMatch[1] : beforeCursor
    // Keep one space between existing text and the inserted mention.
    const sep = head && !/\s$/.test(head) ? ' ' : ''
    const prefix = head + sep + '@' + name + ' '
    onInputChange(prefix + afterCursor)
    requestAnimationFrame(() => {
      const ta = textareaRef.current
      if (ta) {
        ta.setSelectionRange(prefix.length, prefix.length)
        ta.focus()
      }
    })
  }

  // removeMention takes one addressee back out (the picker row toggles). The
  // draft may hold the display name, the id, or both across edits, so every
  // spelling is removed. A mention ends at whitespace or at sentence
  // punctuation — "@ops." is ops followed by a period.
  const removeMention = (id: string) => {
    const w = partners.find(p => p.id === id)
    const spellings = new Set([w ? mentionName(w) : id, id])
    let next = input
    for (const sp of spellings) {
      const token = sp.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
      next = next.replace(new RegExp('(^|\\s)@' + token + '(?=$|[\\s,.;:!?，。；：！？])', 'g'), '$1')
    }
    onInputChange(next.trimStart())
  }

  // Removing a recipient takes it out of every place it lives — the target set
  // and the draft — so one uncheck really drops it (a worker can be both in the
  // target set and @-typed into the draft).
  const removeTarget = (id: string) => {
    if (targets.includes(id)) onTargetsChange(targets.filter(x => x !== id))
    if (mentioned.includes(id)) removeMention(id)
  }

  // Adding one extends the target set only — it does not touch the sidebar
  // selector's range, which is a different thing (what you are watching).
  const addTarget = (id: string) => {
    if (!targets.includes(id)) onTargetsChange([...targets, id])
  }

  // Commit a picker selection. Completing a typed mention rewrites the draft;
  // picking a recipient edits the target set instead. Enter commits without
  // toggling, a click toggles.
  const commitPicker = (id: string, toggle = true) => {
    if (pickerFor === 'target') {
      if (checkedTargets.includes(id)) {
        if (toggle) removeTarget(id)
      } else {
        addTarget(id)
      }
    } else if (toggle && mentioned.includes(id)) {
      removeMention(id)
    } else if (!mentioned.includes(id)) {
      insertMention(id)
    }
    setPickerOpen(false)
    setPickerQuery('')
  }

  // Toggle one recipient in place, without closing anything: the expanded
  // dialog stays open while several workers are checked. Removing takes the id
  // out of both the target set and the draft (one uncheck really drops it);
  // adding extends the target set only.
  const toggleRecipient = (id: string) => {
    if (recipients.includes(id)) removeTarget(id)
    else addTarget(id)
  }

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // IME composition: keys (esp. Enter to confirm pinyin) must not trigger
    // send or mention navigation while composing.
    const composing = e.nativeEvent.isComposing || e.keyCode === 229

    if (pickerOpen && !composing) {
      // Use the same pickable set the dropdown renders (archived/suspended
      // reason workers excluded), so Enter commits exactly the highlighted row.
      const list = pickerWorkers
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
      if (recipients.length === 0) {
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
  const pickableWorkers = useMemo(
    () => partners.filter(w => !archived.has(w.id) && w.state !== 'suspended'),
    [partners, archived],
  )
  // The dropdown's search box matches the display name, the id, the
  // description and the tags; an empty query keeps everything.
  const pickableShown = useMemo(() => {
    const q = pickerQuery.trim().toLowerCase()
    if (!q) return pickableWorkers
    return pickableWorkers.filter(w =>
      w.id.toLowerCase().includes(q) ||
      mentionName(w).toLowerCase().includes(q) ||
      (w.description || '').toLowerCase().includes(q) ||
      (w.tags || []).some(tag => tag.toLowerCase().includes(q)))
  }, [pickableWorkers, pickerQuery])

  // The picker's option list: one flat list, no tag groups. While searching the
  // best hit comes first — exact name, then prefix, then substring — so
  // spelling what you want puts it where the default highlight (and Enter)
  // lands. With no query, the recipients already addressed lead (in the chip's
  // order, so the two line up), then everyone else by display name.
  const pickerWorkers = useMemo(() => {
    const q = pickerQuery.trim().toLowerCase()
    if (q) {
      const rank = (w: WorkerInfo) => {
        const name = mentionName(w).toLowerCase()
        const id = w.id.toLowerCase()
        if (name === q || id === q) return 0
        return name.startsWith(q) || id.startsWith(q) ? 1 : 2
      }
      return pickableShown.slice().sort((a, b) => rank(a) - rank(b))
    }
    const picked = pickerFor === 'target'
      ? checkedTargets.map(id => pickableShown.find(w => w.id === id)).filter((w): w is WorkerInfo => !!w)
      : []
    const pickedIds = new Set(picked.map(w => w.id))
    const rest = pickableShown
      .filter(w => !pickedIds.has(w.id))
      .slice()
      .sort((a, b) => mentionName(a).localeCompare(mentionName(b)))
    return [...picked, ...rest]
  }, [pickableShown, pickerQuery, pickerFor, checkedTargets])

  // Rows read by display name, with the original id right after it when the
  // two differ. No worker type in this list: it is identity metadata nobody
  // picks by, and it crowded the one thing the row is for.
  const pickList: PickerOption[] = pickerWorkers.map(w => ({
    id: w.id,
    label: mentionName(w),
    sublabel: w.label ? w.id : undefined,
    // One line per worker: name + id. The description stays in the tooltip (and
    // in the expanded picker), where it can be read without turning every row
    // into a two-line block.
    hint: w.description,
  }))
  const activeOptionIndex = pickerWorkers.length > 0
    ? Math.min(Math.max(mentionIndex, 0), pickerWorkers.length - 1)
    : -1

  // Recipients: the target set plus whatever the draft @-mentions.
  const body = leadingMentions(input, partners).rest.trim()
  const canSend = recipients.length > 0 && (body !== '' || attachments.length > 0)
  // Chip content: one token per recipient, capped so a big selection cannot
  // stretch the action row (the overflow becomes "+N"). The tooltip always
  // carries the full list.
  const maxTokens = isMobile ? 1 : 2
  const shownTargets = recipients.slice(0, maxTokens)
  const hiddenTargets = recipients.length - shownTargets.length
  // Recipients read by display name (备注名) everywhere: the chip's tokens and
  // its tooltip. The id stays the identity behind them.
  const nameOf = useCallback((id: string) => {
    const w = partners.find(p => p.id === id)
    return w ? mentionName(w) : id
  }, [partners])
  const targetsLabel = recipients.length > 0
    ? recipients.map(id => '@' + nameOf(id)).join(' · ')
    : t('talk.input.noTarget')
  // A recipient appearing (typed @, or a worker checked in the sidebar) answers
  // the refusal hint, so it goes away at once.
  useEffect(() => {
    if (recipients.length > 0) setNoTargetHint(false)
  }, [recipients.length])

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

  // ── Input box resizing ──
  // The input panel is anchored to the bottom of the talk view, so its height
  // is taken from the conversation above: a taller box means a shorter
  // message list. Heights are the textarea's own pixel height.
  //
  // boxState.h is the height the user settled on (null = the textarea's
  // natural 3-row size); the expanded flag is the temporary tall preset.
  // The effective height follows from the two.
  // Clamped at render time too: a height saved on a big screen must not
  // overflow a smaller one.
  const boxH = boxState.expanded
    ? expandedBoxHeight()
    : boxState.h === null ? null : Math.min(boxState.h, maxBoxHeight())
  // The expand button is pointless once the box is already at least as tall
  // as the preset — expanding would shrink it. It stays visible while the
  // preset is on, so the box can be collapsed back.
  const showExpand = boxState.expanded || boxH === null || boxH < expandedBoxHeight()

  // Drag the grip: the panel grows upward, so a pointer that moves up makes
  // the box taller. The start height is the textarea's measured height
  // (its natural 3-row size when no explicit height is set yet).
  const startResize = (e: React.PointerEvent) => {
    e.preventDefault()
    // Capture the pointer so the drag survives leaving the grip (or the
    // window) and cannot be stolen by a scroll gesture.
    ;(e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId)
    const ta = textareaRef.current
    const startH = ta ? ta.offsetHeight : MIN_BOX_H
    resizeRef.current = { startY: e.clientY, startH, h: startH }
    document.body.style.cursor = 'ns-resize'
    document.body.style.userSelect = 'none'
    const onMove = (ev: PointerEvent) => {
      const st = resizeRef.current
      if (!st) return
      const next = st.startH - (ev.clientY - st.startY)
      const h = Math.round(Math.min(maxBoxHeight(), Math.max(MIN_BOX_H, next)))
      // The dragged height is kept in the ref, not read back from state:
      // the last pointermove's setState may not have flushed yet when the
      // pointer is released.
      st.h = h
      // A drag sets the user's own height and leaves the expanded preset.
      // Not persisted here — the drag writes to storage once, when the
      // pointer is released.
      saveBox({ h, expanded: false }, false)
    }
    const onUp = () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      window.removeEventListener('pointercancel', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      // Persist the dragged height now that the drag is over.
      const st = resizeRef.current
      if (st) saveBox({ h: st.h, expanded: false })
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    window.addEventListener('pointercancel', onUp)
  }

  // The expand button toggles the tall preset; collapsing returns the box to
  // the height the user had settled on.
  const toggleExpand = () => {
    saveBox({ h: boxState.h, expanded: !boxState.expanded })
  }

  // Double-clicking the grip restores the textarea's natural size — the way
  // back to the default without hunting for a "reset" button.
  const resetBoxHeight = () => {
    saveBox({ h: null, expanded: false })
  }

  const targetChip = (
    <div style={{ position: 'relative', display: 'flex', minWidth: 0, ...(isMobile ? { flexShrink: 1 } : { marginRight: 'auto' }) }}>
        <span
          onClick={(e) => {
            e.stopPropagation()
            // The chip shows the whole recipient set, so the picker edits that
            // set — the draft is only touched to drop a typed mention.
            setPickerFor('target')
            setPickerQuery('')
            setMentionIndex(0)
            setPickerOpen(v => !v)
          }}
          title={recipients.length > 0
            ? t('talk.input.targets.tooltip', { targets: targetsLabel })
            : t('talk.input.noTarget.tooltip')}
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: actionFont, lineHeight: '20px',
            color: recipients.length > 0 ? colors.accent : colors.textDimmed,
            border: '1px solid ' + (recipients.length > 0 ? colors.accentBorder : colors.border),
            borderRadius: 2, padding: '4px 10px', cursor: 'pointer', userSelect: 'none',
          }}
        >
          {/* Recipients as tokens rather than a comma-separated string. The
              group clips (never wraps) so the mobile button row keeps its
              height; the tooltip lists every recipient. */}
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, minWidth: 0, overflow: 'hidden' }}>
            {recipients.length === 0 ? (
              <span style={{ whiteSpace: 'nowrap' }}>{t('talk.input.noTarget')}</span>
            ) : (
              <>
                {shownTargets.map(id => (
                  <span
                    key={id}
                    style={{
                      display: 'inline-flex', alignItems: 'center', gap: 1,
                      background: colors.bgChip, borderRadius: 2, padding: '0 6px',
                      lineHeight: '18px', whiteSpace: 'nowrap',
                    }}
                  >
                    <span style={{ color: colors.textDimmed }}>@</span>{nameOf(id)}
                  </span>
                ))}
                {hiddenTargets > 0 && (
                  <span style={{ color: colors.textDimmed, whiteSpace: 'nowrap' }}>+{hiddenTargets}</span>
                )}
              </>
            )}
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
              header={pickerFor === 'target' ? t('picker.targets') : t('picker.mention')}
              options={pickList}
              selectedIds={checkedTargets}
              activeIndex={activeOptionIndex}
              onSelect={commitPicker}
              width={isMobile ? 480 : 280}
              onActivate={setMentionIndex}
              onExpand={pickerFor === 'target' ? () => { setPickerOpen(false); setTargetModalOpen(true) } : undefined}
              expandLabel={t('sidebar.workerSelector.expandShort')}
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
      // flexShrink 0: a taller input box takes its room from the message
      // list above, never from the input itself.
      style={{ padding: '0 24px', borderTop: '1px solid ' + colors.border, position: 'relative', flexShrink: 0, paddingBottom: 'calc(12px + env(safe-area-inset-bottom, 0px))', ...(dragging ? { outline: '2px dashed ' + colors.accent, outlineOffset: -2 } : {}) }}
    >
      {/* Drag overlay: a full-area hint shown while files hover over the
          input, so dropping is discoverable. Sits above the text. */}
      {dragging && (
        <div style={{ position: 'absolute', inset: 0, zIndex: 10, display: 'flex', alignItems: 'center', justifyContent: 'center', background: colors.bgChip, pointerEvents: 'none' }}>
          <span style={{ color: colors.accent, fontSize: fontSizes.md, fontWeight: 600 }}>{t('talk.attach.drop')}</span>
        </div>
      )}

      {/* Resize strip: the panel's top border is the drag line, and the
          grip straddles it exactly like the sidebar's grip straddles its
          vertical line (same 5x30 pill). The panel is anchored to the
          bottom, so dragging up makes the box taller. The button at the
          right jumps straight to a tall box (and back); it is bottom-aligned
          so a clear gap separates it from the border above. */}
      <div style={{ position: 'relative', height: isMobile ? 30 : 26, display: 'flex', alignItems: 'flex-end', justifyContent: 'flex-end' }}>
        <div
          onPointerDown={startResize}
          onDoubleClick={resetBoxHeight}
          onMouseEnter={() => setGripHover(true)}
          onMouseLeave={() => setGripHover(false)}
          title={t('talk.input.resize.tooltip')}
          style={{
            // Hit area straddling the panel's top border (which sits 1px
            // above this strip), wide enough to grab on a phone.
            position: 'absolute', top: -3, left: '50%', transform: 'translateX(-50%)',
            width: isMobile ? 56 : 44, height: 13,
            cursor: 'ns-resize', touchAction: 'none', userSelect: 'none',
          }}
        >
          <span style={{
            // The grip pill: 5 thick, 30 long, centered on the panel's 1px
            // top border — the sidebar's grip, turned to lie along it.
            //
            // Geometry: the strip starts right below the panel's 1px border,
            // so the border occupies y -1..0 relative to the strip. The hit
            // area starts 3px above the strip, so the pill at offset 0 lands
            // on y -3..2 — straddling the border's center (-0.5) exactly.
            position: 'absolute', top: 0, left: '50%', transform: 'translateX(-50%)',
            width: 30, height: 5, borderRadius: 3, zIndex: 1,
            background: gripHover ? colors.textDim : colors.textDimmed,
            transition: 'background 0.15s',
          }} />
        </div>
        {showExpand && (
          <button
            onClick={toggleExpand}
            title={boxState.expanded ? t('talk.input.collapse.tooltip') : t('talk.input.expand.tooltip')}
            style={{
              display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
              width: isMobile ? 30 : 24, height: isMobile ? 22 : 18,
              padding: 0, background: 'none',
              border: '1px solid ' + colors.border, borderRadius: 4,
              color: boxState.expanded ? colors.accent : colors.textDimmed,
              cursor: 'pointer', flexShrink: 0,
            }}
          >
            {/* Drawn chevron — glyph baselines sit off-center in the mono font. */}
            <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
              <path
                d={boxState.expanded ? 'M2.5 4.5 6 8l3.5-3.5' : 'M2.5 7.5 6 4l3.5 3.5'}
                stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round"
              />
            </svg>
          </button>
        )}
      </div>

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
          // A dragged/expanded box has an explicit height; without one the
          // textarea keeps its natural 3-row size.
          height: boxH === null ? undefined : boxH,
          overflowY: 'auto',
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
            if (recipients.length === 0) { flashNoTarget(); return }
            if (!canSend) return
            onSend()
          }}
          aria-disabled={!canSend}
          title={canSend ? undefined : recipients.length === 0 ? t('talk.input.send.blocked') : undefined}
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
      {targetModalOpen && (
        // The sidebar's expanded picker, reused: same dialog, same search /
        // tag / online filters, and it toggles the same recipient set the
        // dropdown edits.
        <WorkerPickerModal
          title={t('sidebar.workerSelector')}
          workers={pickableWorkers}
          selected={new Set(recipients)}
          onToggle={toggleRecipient}
          onMention={(id) => { setTargetModalOpen(false); insertMention(id) }}
          onClose={() => setTargetModalOpen(false)}
          isMobile={isMobile}
        />
      )}
    </div>
  )
}