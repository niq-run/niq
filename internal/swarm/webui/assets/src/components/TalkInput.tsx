import { useState, useRef, useEffect, useMemo } from 'react'
import { useTheme, fontSizes } from '../theme'
import PickerDropdown, { type PickerOption } from './PickerDropdown'
import type { WorkerInfo } from '../types'

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
}

export default function TalkInput({ talkPartner, input, inputMode, onInputChange, onSend, onAbort, onModeChange, workers, archived, mentionKey, mentionTarget, onClearMentionTarget, onSelectTarget }: TalkInputProps) {
  const { colors } = useTheme()
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [pickerMode, setPickerMode] = useState<'mention' | 'target'>('target')
  const [mentionQuery, setMentionQuery] = useState('')
  const [mentionIndex, setMentionIndex] = useState(0)
  const [modeOpen, setModeOpen] = useState(false)

  const reasonWorkers = useMemo(() => workers.filter(w => w.type === 'reason'), [workers])

  const handleChange = (e: React.ChangeEvent<HTMLTextAreaElement>) => {
    const val = e.target.value
    onInputChange(val)

    // Detect if we're typing an @mention
    const cursorPos = e.target.selectionStart
    const beforeCursor = val.slice(0, cursorPos)
    const atMatch = beforeCursor.match(/@(\w*)$/)
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
    const atMatch = beforeCursor.match(/^(.*)@\w*$/)
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
      const list = pickerMode === 'mention'
        ? reasonWorkers.filter(w => w.id.toLowerCase().includes(mentionQuery))
        : reasonWorkers
      if ((e.ctrlKey && e.key.toLowerCase() === 'n') || e.key === 'ArrowDown') {
        e.preventDefault()
        setMentionIndex(i => Math.min(i + 1, list.length - 1))
        return
      }
      if ((e.ctrlKey && e.key.toLowerCase() === 'p') || e.key === 'ArrowUp') {
        e.preventDefault()
        setMentionIndex(i => Math.max(i - 1, 0))
        return
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
  // position (not only at the start), showing the most recent one.
  const currentTarget = useMemo(() => {
    const matches = [...input.matchAll(/@(\w+)/g)]
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
  const pickList = (pickerOpen && pickerMode === 'mention')
    ? pickableWorkers.filter(w => w.id.toLowerCase().includes(mentionQuery))
    : pickableWorkers

  // Persistent target: a specific reason worker, or '' (broadcast).
  const persistentTarget = mentionTarget && reasonWorkers.some(r => r.id === mentionTarget) ? mentionTarget : ''
  // The single visible target: an immediate @ in the input takes priority, then
  // the persistent target, otherwise broadcast.
  const shownTarget = currentTarget || persistentTarget

  // Input-mode selector options. Descriptions condensed from pkg/reason:
  //   interrupt = level 3 (cancel in-flight reasoning, handle now)
  //   schedule  = level 2 (no interrupt; respond promptly next round)
  //   append    = level 1 (only when idle; least intrusive)
  const modeOptions: PickerOption[] = [
    { id: 'default', label: 'interrupt', description: 'cancel in-flight reasoning and handle now', hint: "interrupt in-flight reasoning and handle now (level 3)" },
    { id: 'schedule', label: 'schedule', description: 'no interrupt; respond promptly next round', hint: "wake gently; don't interrupt in-flight reasoning (level 2)" },
    { id: 'append', label: 'append', description: 'only when idle; least intrusive', hint: "only when idle (no reasoning, no pending tools) (level 1)" },
  ]
  const currentModeLabel = modeOptions.find(m => m.id === inputMode)?.label ?? inputMode

  return (
    <div style={{ padding: '12px 24px', borderTop: '1px solid ' + colors.border, position: 'relative' }}>
      {/* Target indicator: a single always-on chip reflecting the current target
          (an immediate @ in the input takes priority, else the persistent target,
          else broadcast). Click to open the target/mention picker. */}
      <div style={{ position: 'relative', display: 'inline-block', marginBottom: 4 }}>
        <span
          onClick={(e) => { e.stopPropagation(); setPickerMode('target'); setPickerOpen(v => !v) }}
          title={shownTarget ? `targeting ${shownTarget} — click to change` : 'broadcasting; click to target a worker'}
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: fontSizes.sm, lineHeight: '16px',
            color: shownTarget ? colors.accent : colors.textDimmed,
            border: '1px solid ' + (shownTarget ? colors.accentBorder : colors.border),
            borderRadius: 4, padding: '1px 8px', cursor: 'pointer', userSelect: 'none',
          }}
        >
          {shownTarget ? `→ ${shownTarget}` : '→ broadcast'}
        </span>

        {pickerOpen && (
          <div style={{ position: 'absolute', left: 0, bottom: '100%', marginBottom: 4, zIndex: 100 }}>
            <PickerDropdown
              header={pickerMode === 'mention' ? 'mention' : 'target'}
              options={pickList.map(w => ({ id: w.id, label: w.id, sublabel: w.type }))}
              selectedId={pickerMode === 'target' ? (shownTarget || undefined) : undefined}
              activeIndex={pickerMode === 'mention' ? mentionIndex : undefined}
              onSelect={commitPicker}
              onActivate={(i) => { if (pickerMode === 'mention') setMentionIndex(i) }}
              footer={pickerMode !== 'mention' ? {
                label: 'broadcast (no target)',
                checked: !shownTarget,
                onChoose: () => { onClearMentionTarget(); setPickerOpen(false) },
              } : undefined}
            />
          </div>
        )}
      </div>

      <textarea
        ref={textareaRef}
        value={input}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        placeholder='Type a message... (@ to mention a worker, Shift+Enter for new line)'
        rows={3}
        style={{
          width: '100%',
          background: 'transparent',
          color: colors.text,
          border: 'none',
          outline: 'none',
          padding: '8px 0',
          fontSize: 14,
          fontFamily: 'monospace',
          resize: 'none',
          lineHeight: 1.5,
          boxSizing: 'border-box',
        }}
      />
      <div style={{ display: 'flex', gap: 12, justifyContent: 'flex-end', alignItems: 'center' }}>
        <div style={{ position: 'relative', display: 'inline-block' }}>
          <span
            onClick={(e) => { e.stopPropagation(); setModeOpen(v => !v) }}
            title={modeOptions.find(m => m.id === inputMode)?.hint}
            style={{
              display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: fontSizes.base, lineHeight: '18px',
              color: colors.textDim,
              border: '1px solid ' + colors.border, borderRadius: 4, padding: '1px 8px',
              cursor: 'pointer', userSelect: 'none',
            }}
          >
            {`mode: ${currentModeLabel}`}
            <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>▾</span>
          </span>
          {modeOpen && (
            <div style={{ position: 'absolute', right: 0, bottom: '100%', marginBottom: 4, zIndex: 100 }}>
              <PickerDropdown
                header="mode"
                options={modeOptions}
                selectedId={inputMode}
                onSelect={(id) => { onModeChange(id); setModeOpen(false) }}
                width={420}
              />
            </div>
          )}
        </div>
        <button
          onClick={onAbort}
          className="btn-stop"
          style={{
            background: 'none',
            color: colors.textDim,
            border: 'none',
            padding: '4px 12px',
            borderRadius: 4,
            cursor: 'pointer',
            fontSize: 13,
            fontFamily: 'monospace',
          }}
        >
          Stop
        </button>
        <button
          onClick={onSend}
          className="btn-send"
          style={{
            background: 'none',
            color: colors.accent,
            border: 'none',
            padding: '4px 12px',
            borderRadius: 4,
            cursor: 'pointer',
            fontSize: 13,
            fontWeight: 'bold',
            fontFamily: 'monospace',
          }}
        >
          Send
        </button>
      </div>
    </div>
  )
}