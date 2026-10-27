import { createContext, useContext, useState, useCallback, useEffect, type ReactNode } from 'react'

// ── Font size scale (min 12px for readability) ──
// VIEW_HEADER_HEIGHT is the shared height of every main-area view header AND
// the sidebar's logo band: the two bottom borders must stay on one line.
export const VIEW_HEADER_HEIGHT = 56

export const fontSizes = {
  xs: 12,   // timestamps, metadata, secondary labels
  sm: 13,   // tool call summaries, detail panels
  base: 14, // thinking content, body text
  md: 14,   // inputs, buttons
  lg: 16,   // section headers
  xl: 18,   // sub-headers
  xxl: 20,  // larger headings
  h2: 28,   // logo / main heading
} as const

export type FontSizeKey = keyof typeof fontSizes

// ── Palette ──
export interface Palette {
  bg: string
  bgLight: string
  bgLighter: string
  bgChip: string
  border: string
  borderLight: string
  text: string
  textMuted: string
  textDim: string
  textDimmed: string
  accent: string
  accentDim: string
  accentBg: string
  accentBorder: string
  // Tool status
  toolRequested: string
  toolCompleted: string
  toolFailed: string
  // Warning / "needs your attention" tone (inline hints). Deliberately a muted
  // amber rather than a bright yellow: the palette is desaturated throughout, so
  // this reads as a quiet nudge instead of an alarm. Tuned per theme to stay
  // legible (≈5:1) on each background.
  warn: string
  // Event-type tag colours encode *outcome*, not family, so the stream can be
  // scanned for what finished and what broke. Same recipe as the worker-type
  // tags below — same saturation, lightness tuned per hue so every tag lands
  // at a common contrast against the row background — and the tag itself is
  // the colour at 12% (background) / 33% (border) over the row background.
  //
  //   completed → green    request.completed, *.done, *.succeeded
  //   failed    → red      *.failed, *.rejected, *.error
  //   reason    → purple   reason.* (always purple, even when it fails)
  //   request   → yellow   request.*, tool.*, *.request, *.call
  //   other     → grey     lifecycle / infrastructure / everything else
  eventType: {
    completed: string
    failed: string
    reason: string
    request: string
    other: string
  }
  // Worker-type tag colours: one shared by every built-in worker, one for
  // reason workers, one for coding workers, and one shared by every external
  // (third-party) worker. The tag text names the type; colour only separates
  // these groups.
  typeTag: {
    builtin: string
    reason: string
    coding: string
    external: string
  }
  // Worker / target labels
  workerId: string
  eventRowBorder: string
  eventRowTime: string
  eventRowTarget: string
  eventRowSummary: string
  // Detail panel
  detailBg: string
  detailLabel: string
  detailValue: string
  detailBorder: string
}

const dark: Palette = {
  bg: '#2a2a2a',
  bgLight: '#383838',
  bgLighter: '#404040',
  bgChip: '#303030',
  border: '#525252',
  borderLight: '#434343',
  text: '#e0e0e0',
  textMuted: '#ccc',
  textDim: '#888',
  textDimmed: '#707070',
  accent: 'rgb(60,120,180)',
  accentDim: 'rgba(60,120,180,0.6)',
  accentBg: 'rgba(60,120,180,0.15)',
  accentBorder: 'rgba(60,120,180,0.25)',
  toolRequested: '#c88a3a',
  toolCompleted: '#5a8a5a',
  toolFailed: '#f44336',
  warn: '#a08a52',
  eventType: {
    completed: '#4bbb70',  // green      (hue 140)
    failed: '#ea8c89',     // salmon red (hue   2)
    reason: '#a99fd8',     // periwinkle (hue 250)
    request: '#c5a239',    // gold       (hue  45)
    other: '#9da2ab',      // slate      (hue 220)
  },
  typeTag: {
    builtin: '#8fa9d1',
    reason: '#c896d4',
    coding: '#67b953',
    external: '#c4a26e',
  },
  workerId: '#1f7c22',
  eventRowBorder: '#383838',
  eventRowTime: '#707070',
  eventRowTarget: '#1f7c22',
  eventRowSummary: '#1f7c22',
  detailBg: '#383838',
  detailLabel: '#777',
  detailValue: '#ccc',
  detailBorder: '#434343',
}

const light: Palette = {
  bg: '#ebebeb',
  bgLight: '#dbdbdb',
  bgLighter: '#d1d1d1',
  bgChip: '#e0e0e0',
  border: '#c3c3c3',
  borderLight: '#cdcdcd',
  text: '#1a1a1a',
  textMuted: '#333',
  textDim: '#666',
  textDimmed: '#909090',
  accent: 'rgb(60,120,180)',
  accentDim: 'rgba(60,120,180,0.6)',
  accentBg: 'rgba(60,120,180,0.1)',
  accentBorder: 'rgba(60,120,180,0.2)',
  toolRequested: '#c88a3a',
  toolCompleted: '#5a8a5a',
  toolFailed: '#f44336',
  warn: '#7f6c36',
  eventType: {
    completed: '#27673c',  // green      (hue 140)
    failed: '#af241f',     // red        (hue   2)
    reason: '#5b48ba',     // periwinkle (hue 250)
    request: '#6c591f',    // gold       (hue  45)
    other: '#575c66',      // slate      (hue 220)
  },
  typeTag: {
    builtin: '#375c92',
    reason: '#863a99',
    coding: '#336627',
    external: '#73562b',
  },
  workerId: '#1b5e20',
  eventRowBorder: '#d0d0d0',
  eventRowTime: '#909090',
  eventRowTarget: '#1b5e20',
  eventRowSummary: '#1b5e20',
  detailBg: '#e3e3e3',
  detailLabel: '#888',
  detailValue: '#444',
  detailBorder: '#c3c3c3',
}

interface ThemeCtx {
  dark: boolean
  toggle: () => void
  inverted: boolean
  toggleInverted: () => void
  colors: Palette
}

const ThemeContext = createContext<ThemeCtx>({ dark: true, toggle: () => {}, inverted: false, toggleInverted: () => {}, colors: dark })

function getInitialDark(): boolean {
  const stored = globalThis.localStorage?.getItem('niq-theme')
  if (stored === 'dark') return true
  if (stored === 'light') return false
  if (globalThis.matchMedia?.('(prefers-color-scheme: dark)').matches) return true
  return false
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [isDark, setIsDark] = useState(getInitialDark)
  const [inverted, setInverted] = useState(false)

  // Follow live OS theme switches, but only while the user has not made an
  // explicit choice — a stored 'niq-theme' is authoritative.
  useEffect(() => {
    if (!globalThis.matchMedia) return
    const mq = globalThis.matchMedia('(prefers-color-scheme: dark)')
    const onChange = (e: MediaQueryListEvent) => {
      let pinned = false
      try { pinned = !!globalThis.localStorage?.getItem('niq-theme') } catch { /* ignore */ }
      if (!pinned) setIsDark(e.matches)
    }
    mq.addEventListener('change', onChange)
    return () => mq.removeEventListener('change', onChange)
  }, [])

  // Persist only explicit choices: the system fallback must not pin the
  // last-observed value into localStorage.
  const toggle = useCallback(() => setIsDark(d => {
    const next = !d
    globalThis.localStorage?.setItem('niq-theme', next ? 'dark' : 'light')
    return next
  }), [])

  // The inverted (colourful) mode: toggles the global inverted flag. Only the
  // handful of structural border lines explicitly opt in (sidebar divider /
  // partitions / band bottom / input divider); content, inputs and buttons are
  // left untouched.
  const toggleInverted = useCallback(() => setInverted(v => !v), [])
  const colors = isDark ? dark : light
  return (
    <ThemeContext.Provider value={{ dark: isDark, toggle, inverted, toggleInverted, colors }}>
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme(): ThemeCtx {
  return useContext(ThemeContext)
}
