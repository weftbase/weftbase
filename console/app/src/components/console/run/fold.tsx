import { ChevronRightIcon } from "lucide-react"
import * as React from "react"

import { usePref } from "@/lib/prefs"
import { cn } from "@/lib/utils"

/** Fold: a titled, collapsible section of a detail. With `id` its state
 * is remembered across rows and visits; without, it starts as `open`.
 *
 *   <Fold id="detail.input" title="Input">…</Fold> */
export function Fold({
  id,
  title,
  hint,
  action,
  open: initial = true,
  children,
  className,
}: {
  id?: string
  title: React.ReactNode
  /** One muted word or figure after the title ("3 messages", "1.2 kB"). */
  hint?: React.ReactNode
  action?: React.ReactNode
  open?: boolean
  children: React.ReactNode
  className?: string
}) {
  const [stored, setStored] = usePref(
    `wb.fold.${id ?? "-"}`,
    initial ? "1" : "0"
  )
  const [local, setLocal] = React.useState(initial)
  const open = id ? stored === "1" : local
  const toggle = () => (id ? setStored(open ? "0" : "1") : setLocal(!open))
  return (
    <section className={cn("border-b last:border-b-0", className)}>
      <div className="flex h-8 items-center gap-2 pr-3 pl-2">
        <button
          type="button"
          aria-expanded={open}
          onClick={toggle}
          className="flex h-6 min-w-0 flex-1 items-center gap-1 rounded-sm text-left outline-none focus-visible:outline-2 focus-visible:outline-ring"
        >
          <ChevronRightIcon
            aria-hidden
            className={cn(
              "size-3.5 flex-none text-subtle-foreground transition-transform",
              open && "rotate-90"
            )}
          />
          <h3 className="table-head">{title}</h3>
          {hint !== undefined && hint !== null && hint !== "" && (
            <span className="truncate text-xs text-subtle-foreground">
              {hint}
            </span>
          )}
        </button>
        {action}
      </div>
      {open && <div className="flex flex-col gap-2 px-3 pb-3">{children}</div>}
    </section>
  )
}

/** Inline: a small unremembered disclosure inside a section ("12
 * earlier messages", "Tools offered · 6"). */
export function Inline({
  label,
  open: initial = false,
  children,
  className,
}: {
  label: React.ReactNode
  open?: boolean
  children: React.ReactNode
  className?: string
}) {
  const [open, setOpen] = React.useState(initial)
  return (
    <div className={cn("min-w-0", className)}>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="flex h-6 max-w-full items-center gap-1 rounded-sm pr-1.5 text-left text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
      >
        <ChevronRightIcon
          aria-hidden
          className={cn(
            "size-3 flex-none text-subtle-foreground transition-transform",
            open && "rotate-90"
          )}
        />
        <span className="truncate">{label}</span>
      </button>
      {open && <div className="mt-1 flex flex-col gap-2">{children}</div>}
    </div>
  )
}

/** Facts: label / value rows of a detail. */
export function Facts({
  rows,
  className,
}: {
  rows: (readonly [string, React.ReactNode] | false | null | undefined)[]
  className?: string
}) {
  const shown = rows.filter(
    (r): r is readonly [string, React.ReactNode] =>
      !!r && r[1] !== undefined && r[1] !== null && r[1] !== ""
  )
  if (shown.length === 0) return null
  return (
    <dl
      className={cn(
        "grid grid-cols-[minmax(96px,auto)_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs",
        className
      )}
    >
      {shown.map(([k, v]) => (
        <React.Fragment key={k}>
          <dt className="text-muted-foreground">{k}</dt>
          <dd className="min-w-0 break-words">{v}</dd>
        </React.Fragment>
      ))}
    </dl>
  )
}

/** Text: a block of recorded text (a prompt, a reply, a tool result). */
export function Text({
  children,
  mono = false,
  tone,
  className,
}: {
  children: React.ReactNode
  mono?: boolean
  tone?: "danger"
  className?: string
}) {
  return (
    <div
      className={cn(
        "max-h-80 overflow-auto rounded-input border bg-surface px-2 py-1.5 text-sm break-words whitespace-pre-wrap",
        mono && "font-mono text-xs leading-[18px]",
        tone === "danger" &&
          "border-danger/30 bg-danger-muted text-danger-foreground",
        className
      )}
    >
      {children}
    </div>
  )
}

/** Muted: one line saying something is absent, and why. */
export function Muted({ children }: { children: React.ReactNode }) {
  return <p className="text-xs text-muted-foreground">{children}</p>
}
