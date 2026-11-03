// The theme: "system", "light" or "dark", kept in localStorage under
// "theme" (the same key as weftbase.ai). The bootstrap script sets the
// class before first paint; it is inline, so the build records its
// sha256 in dist/csp.json.

import { useSyncExternalStore } from "react"

export type ThemePref = "system" | "light" | "dark"

export const themeBootstrap = `(function(){var t;try{t=localStorage.getItem("theme")}catch(e){}var d=t==="dark"||(t!=="light"&&matchMedia("(prefers-color-scheme: dark)").matches);var h=document.documentElement;h.classList.toggle("dark",d);h.style.colorScheme=d?"dark":"light"})()`

const listeners = new Set<() => void>()

function read(): ThemePref {
  try {
    const t = localStorage.getItem("theme")
    return t === "light" || t === "dark" ? t : "system"
  } catch {
    return "system"
  }
}

function systemDark(): boolean {
  return (
    typeof matchMedia !== "undefined" &&
    matchMedia("(prefers-color-scheme: dark)").matches
  )
}

export function applyTheme(pref: ThemePref) {
  const dark = pref === "dark" || (pref === "system" && systemDark())
  const h = document.documentElement
  h.classList.toggle("dark", dark)
  h.style.colorScheme = dark ? "dark" : "light"
}

export function setTheme(pref: ThemePref) {
  try {
    if (pref === "system") localStorage.removeItem("theme")
    else localStorage.setItem("theme", pref)
  } catch {
    // storage blocked: the choice lasts for this page only
  }
  applyTheme(pref)
  listeners.forEach((l) => l())
}

function subscribe(l: () => void) {
  listeners.add(l)
  const mq =
    typeof matchMedia !== "undefined"
      ? matchMedia("(prefers-color-scheme: dark)")
      : null
  const onSystem = () => {
    if (read() === "system") applyTheme("system")
    l()
  }
  mq?.addEventListener("change", onSystem)
  return () => {
    listeners.delete(l)
    mq?.removeEventListener("change", onSystem)
  }
}

/** useTheme returns the stored preference and whether dark is showing. */
export function useTheme(): { pref: ThemePref; dark: boolean } {
  const pref = useSyncExternalStore(subscribe, read, () => "system" as const)
  const dark = useSyncExternalStore(
    subscribe,
    () => document.documentElement.classList.contains("dark"),
    () => false
  )
  return { pref, dark }
}
