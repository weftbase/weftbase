import * as React from "react"

import { ago, dateTime } from "@/lib/format"

// One clock for every RelativeTime on the page: a tick every 10 s.
const listeners = new Set<() => void>()
let now = Date.now()
let timer: ReturnType<typeof setInterval> | undefined

function subscribe(l: () => void) {
  listeners.add(l)
  timer ??= setInterval(() => {
    now = Date.now()
    listeners.forEach((f) => f())
  }, 10_000)
  return () => {
    listeners.delete(l)
    if (listeners.size === 0) {
      clearInterval(timer)
      timer = undefined
    }
  }
}

/** useNow is the shared clock (ms), for durations of things still going. */
export function useNow(): number {
  return React.useSyncExternalStore(
    subscribe,
    () => now,
    () => now
  )
}

/** RelativeTime: "5m ago", kept current; the exact time is the title.
 * `sentence`: it ends a sentence ("Approved by you just now"), where
 * a table cell's "now" reads "just now". */
export function RelativeTime({
  time,
  className,
  sentence = false,
}: {
  time: string | null | undefined
  className?: string
  sentence?: boolean
}) {
  const t = useNow()
  if (!time) return <span className={className}>—</span>
  const text = ago(time, Math.max(t, Date.now()))
  return (
    <time dateTime={time} title={dateTime(time)} className={className}>
      {sentence && text === "now" ? "just now" : text}
    </time>
  )
}
