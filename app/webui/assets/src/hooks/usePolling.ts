import { useEffect, useRef } from 'react'

/**
 * Periodically fetches a URL and calls onData with the parsed JSON.
 * Cleans up the interval on unmount.
 *
 * onData is held in a ref: callers may pass an inline closure without
 * re-subscribing the effect (and re-firing an immediate fetch) on every
 * render — only the url / interval / enabled flags (re)start polling.
 */
export function usePolling<T>(url: string, intervalMs: number, onData: (data: T) => void, enabled = true): void {
  const onDataRef = useRef(onData)
  onDataRef.current = onData

  useEffect(() => {
    if (!enabled) return
    let active = true
    const load = async () => {
      try {
        const res = await fetch(url)
        const data = await res.json()
        if (active) onDataRef.current(data)
      } catch {
        // ignore fetch errors for polling endpoints
      }
    }
    load()
    const id = setInterval(load, intervalMs)
    return () => {
      active = false
      clearInterval(id)
    }
  }, [url, intervalMs, enabled])
}
