// Small per-browser preferences (never credentials): kept in
// localStorage, readable before React hydrates.

import { useSyncExternalStore } from "react"

const listeners = new Set<() => void>()

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function write(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key)
    else localStorage.setItem(key, value)
  } catch {
    // storage blocked: the choice lasts for this page only
  }
  listeners.forEach((l) => l())
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => listeners.delete(l)
}

/** usePref is a string preference with a default; set(null) clears it. */
export function usePref(
  key: string,
  fallback: string
): [string, (v: string | null) => void] {
  const value = useSyncExternalStore(
    subscribe,
    () => read(key) ?? fallback,
    () => fallback
  )
  return [value, (v) => write(key, v)]
}
