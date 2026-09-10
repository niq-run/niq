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
  // Event type colors
  eventType: {
    tool: string
    reason: string
    worker: string
    hiw: string
    timer: string
    default: string
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
  bg: '#222',
  bgLight: '#303030',
  bgLighter: '#373737',
  bgChip: '#282828',
  border: '#4a4a4a',
  borderLight: '#383838',
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
  eventType: {
    tool: '#ff9800',
    reason: '#8f7393',
    worker: '#2196f3',
    hiw: '#4caf50',
    timer: '#00bcd4',
    default: '#e0e0e0',
  },
  workerId: '#1f7c22',
  eventRowBorder: '#2d2d2d',
  eventRowTime: '#707070',
  eventRowTarget: '#1f7c22',
  eventRowSummary: '#1f7c22',
  detailBg: '#2d2d2d',
  detailLabel: '#777',
  detailValue: '#ccc',
  detailBorder: '#383838',
}

const light: Palette = {
  bg: '#f8f8f8',
  bgLight: '#eeeeee',
  bgLighter: '#e4e4e4',
  bgChip: '#eaeaea',
  border: '#d6d6d6',
  borderLight: '#dbdbdb',
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
  eventType: {
    tool: '#e65100',
    reason: '#7b1fa2',
    worker: '#1565c0',
    hiw: '#2e7d32',
    timer: '#00838f',
    default: '#1a1a1a',
  },
  workerId: '#1b5e20',
  eventRowBorder: '#dbdbdb',
  eventRowTime: '#909090',
  eventRowTarget: '#1b5e20',
  eventRowSummary: '#1b5e20',
  detailBg: '#f0f0f0',
  detailLabel: '#888',
  detailValue: '#444',
  detailBorder: '#d6d6d6',
}

interface ThemeCtx {
  dark: boolean
  toggle: () => void
  colors: Palette
}

const ThemeContext = createContext<ThemeCtx>({ dark: true, toggle: () => {}, colors: dark })

function getInitialDark(): boolean {
  const stored = globalThis.localStorage?.getItem('niq-theme')
  if (stored === 'dark') return true
  if (stored === 'light') return false
  if (globalThis.matchMedia?.('(prefers-color-scheme: dark)').matches) return true
  return false
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [isDark, setIsDark] = useState(getInitialDark)

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
  return (
    <ThemeContext.Provider value={{ dark: isDark, toggle, colors: isDark ? dark : light }}>
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme(): ThemeCtx {
  return useContext(ThemeContext)
}
