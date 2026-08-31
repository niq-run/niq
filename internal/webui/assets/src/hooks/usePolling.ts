import { useEffect } from 'react'

/**
 * Periodically fetches a URL and calls onData with the parsed JSON.
 * Cleans up the interval on unmount.
 */
export function usePolling<T>(url: string, intervalMs: number, onData: (data: T) => void, enabled = true): void {
  useEffect(() => {
    if (!enabled) return
    let active = true
    const load = async () => {
      try {
        const res = await fetch(url)
        const data = await res.json()
        if (active) onData(data)
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
  }, [url, intervalMs, onData, enabled])
}
