import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { en } from './en'
import { zh } from './zh'

export type StringKey = keyof typeof en
export type Lang = 'en' | 'zh'

const STORAGE_KEY = 'niq-lang'

type Vars = Record<string, string | number>

interface I18nCtx {
  lang: Lang
  setLang: (l: Lang) => void
  t: (key: StringKey, vars?: Vars) => string
}

const Ctx = createContext<I18nCtx | null>(null)

function readInitialLang(): Lang {
  try {
    const v = globalThis.localStorage?.getItem(STORAGE_KEY)
    return v === 'zh' || v === 'en' ? v : 'en'
  } catch {
    return 'en'
  }
}

function interpolate(s: string, vars?: Vars): string {
  if (!vars) return s
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m))
}

export function LanguageProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(readInitialLang)

  useEffect(() => {
    document.documentElement.lang = lang
  }, [lang])

  const setLang = (l: Lang) => {
    setLangState(l)
    try {
      globalThis.localStorage?.setItem(STORAGE_KEY, l)
    } catch {
      // ignore persistence failure (e.g. private mode)
    }
  }

  const t = (key: StringKey, vars?: Vars): string => {
    // Missing key falls back to English; never show the raw key name.
    const s = (lang === 'zh' ? zh : en)[key] ?? en[key]
    return interpolate(s, vars)
  }

  return <Ctx.Provider value={{ lang, setLang, t }}>{children}</Ctx.Provider>
}

export function useI18n(): I18nCtx {
  const ctx = useContext(Ctx)
  if (!ctx) throw new Error('useI18n must be used within LanguageProvider')
  return ctx
}
