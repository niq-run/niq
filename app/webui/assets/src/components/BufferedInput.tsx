import { useEffect, useState, type ChangeEvent } from 'react'

// BufferedInput keeps its own text buffer while being edited, so derived
// values (a mounts array joined into "a, b", env lines) never clobber
// mid-typing: commits go up on every keystroke, but the displayed text only
// syncs from the committed value after blur (or an external change while
// untouched). as="textarea" switches the element.
export default function BufferedInput({ value, onText, as, ...rest }: {
  value: string
  onText: (v: string) => void
  as?: 'textarea'
} & Record<string, any>) {
  const [text, setText] = useState(value)
  const [dirty, setDirty] = useState(false)
  useEffect(() => {
    if (!dirty) setText(value)
  }, [value, dirty])
  const Tag = (as || 'input') as any
  return (
    <Tag
      {...rest}
      value={text}
      onChange={(e: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
        setText(e.target.value)
        setDirty(true)
        onText(e.target.value)
      }}
      onBlur={() => setDirty(false)}
      spellCheck={false}
    />
  )
}

// envToText / textToEnv convert between an env/headers object and KEY=VALUE
// lines (the visual-editor spelling for map-shaped fields).
export function envToText(env: any): string {
  return Object.entries(env || {}).map(([k, v]) => `${k}=${v}`).join('\n')
}
export function textToMap(text: string): Record<string, string> {
  const m: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const i = line.indexOf('=')
    if (i > 0) m[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return m
}
