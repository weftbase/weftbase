import { SearchIcon, XIcon } from "lucide-react"
import * as React from "react"

import { Kbd } from "@/components/ui/kbd"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"

// One filter bar per list: a search box, then selects, then anything on
// the right (a count, a Live pill). 40px high; every value lives in the
// URL (lib/search.ts), the bar only reports changes.
//
//   <FilterBar right={<span>{total} runs</span>}>
//     <SearchInput value={search.q} onChange={(q) => set({ q })} placeholder="Search runs" />
//     <FilterSelect label="Status" value={search.status} options={…} onChange={(status) => set({ status })} />
//   </FilterBar>
export function FilterBar({
  children,
  right,
  className,
}: {
  children: React.ReactNode
  right?: React.ReactNode
  className?: string
}) {
  return (
    <div
      role="search"
      className={cn(
        "flex h-10 flex-none items-center gap-2 overflow-x-auto px-4",
        className
      )}
    >
      {children}
      {right && (
        <div className="ml-auto flex flex-none items-center gap-2 text-xs text-muted-foreground">
          {right}
        </div>
      )}
    </div>
  )
}

const typing = (el: EventTarget | null) =>
  el instanceof HTMLElement &&
  (el.tagName === "INPUT" ||
    el.tagName === "TEXTAREA" ||
    el.tagName === "SELECT" ||
    el.isContentEditable)

/** SearchInput: the list's search box. Typing commits after 250 ms and
 * on Enter; Esc clears. "/" focuses it from anywhere on the page. */
export function SearchInput({
  value,
  onChange,
  placeholder = "Search",
  className,
}: {
  value: string | undefined
  onChange: (value: string | undefined) => void
  placeholder?: string
  className?: string
}) {
  const [text, setText] = React.useState(value ?? "")
  const ref = React.useRef<HTMLInputElement>(null)
  const committed = React.useRef(value ?? "")
  const commit = React.useEffectEvent((next: string) => {
    if (next === committed.current) return
    committed.current = next
    onChange(next.trim() === "" ? undefined : next.trim())
  })

  // The URL changed under us (back, a link): follow it.
  React.useEffect(() => {
    if ((value ?? "") !== committed.current) {
      committed.current = value ?? ""
      setText(value ?? "")
    }
  }, [value])

  React.useEffect(() => {
    const t = setTimeout(() => commit(text), 250)
    return () => clearTimeout(t)
  }, [text])

  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "/" && !e.metaKey && !e.ctrlKey && !typing(e.target)) {
        e.preventDefault()
        ref.current?.focus()
        ref.current?.select()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  return (
    <div
      className={cn(
        "flex h-7 w-64 flex-none items-center gap-1.5 rounded-input border border-input bg-input-background px-2 shadow-input focus-within:border-ring focus-within:shadow-focus",
        className
      )}
    >
      <SearchIcon className="size-3.5 flex-none text-subtle-foreground" />
      <input
        ref={ref}
        type="search"
        value={text}
        placeholder={placeholder}
        aria-label={placeholder}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") commit(text)
          if (e.key === "Escape") {
            if (text) setText("")
            else ref.current?.blur()
          }
        }}
        className="min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-subtle-foreground [&::-webkit-search-cancel-button]:hidden"
      />
      {text ? (
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => {
            setText("")
            ref.current?.focus()
          }}
          className="flex-none rounded-sm text-subtle-foreground hover:text-foreground"
        >
          <XIcon className="size-3.5" />
        </button>
      ) : (
        <Kbd size="sm">/</Kbd>
      )}
    </div>
  )
}

const ANY = "__any__"

/** FilterSelect: one filter. Its first option is "any" (the label), and
 * choosing it reports undefined. */
export function FilterSelect<T extends string>({
  label,
  value,
  options,
  onChange,
  any = `Any ${label.toLowerCase()}`,
  className,
}: {
  label: string
  value: T | undefined
  options: readonly { value: T; label: string }[]
  onChange: (value: T | undefined) => void
  /** The text of the "no filter" option; false for a select that always
   * has a value (a time window). */
  any?: string | false
  className?: string
}) {
  return (
    <Select
      value={value ?? ANY}
      onValueChange={(v) => onChange(v === ANY ? undefined : (v as T))}
    >
      <SelectTrigger
        aria-label={label}
        className={cn(
          "flex-none",
          value === undefined && "text-muted-foreground",
          className
        )}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent position="popper" align="start">
        {any !== false && <SelectItem value={ANY}>{any}</SelectItem>}
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
