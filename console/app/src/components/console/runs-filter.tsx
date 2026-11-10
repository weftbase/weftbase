import { PlusIcon, XIcon } from "lucide-react"
import * as React from "react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { usd } from "@/lib/format"
import type { RunsSearch } from "@/lib/search"

// The Runs list's "+ Filter" menu and the chips of what it applied.
// Filters with a known set of values (tool, model, error kind) are
// submenus; the others (min cost, min tokens, tag, session) open a small
// inline field in the bar. Every value is a URL search param.

type TextKey = "min_cost" | "min_tokens" | "tag" | "session"

const TEXT: Record<
  TextKey,
  { menu: string; label: string; placeholder: string; numeric?: boolean }
> = {
  min_cost: {
    menu: "Min cost…",
    label: "cost ≥ $",
    placeholder: "0.01",
    numeric: true,
  },
  min_tokens: {
    menu: "Min tokens…",
    label: "tokens ≥",
    placeholder: "1000",
    numeric: true,
  },
  tag: { menu: "Tag…", label: "tag", placeholder: "key=value" },
  session: { menu: "Session…", label: "session", placeholder: "session id" },
}

/** parseFilter turns what was typed into the search patch, or undefined
 * when it is not a value the filter takes. */
export function parseFilter(
  key: TextKey,
  text: string
): Partial<RunsSearch> | undefined {
  const t = text.trim()
  if (t === "") return undefined
  switch (key) {
    case "min_cost": {
      const n = Number(t.replace(/^\$/, ""))
      return Number.isFinite(n) && n > 0 ? { min_cost: n } : undefined
    }
    case "min_tokens": {
      const n = Number(t.replace(/[,_ ]/g, ""))
      return Number.isInteger(n) && n > 0 ? { min_tokens: n } : undefined
    }
    case "tag": {
      const eq = t.indexOf("=")
      return eq > 0 && eq < t.length - 1 ? { tag: t } : undefined
    }
    case "session":
      return { session: t }
  }
}

export interface FilterChip {
  key: keyof RunsSearch
  label: string
}

/** filterChips lists the applied "+ Filter" filters, in menu order. */
export function filterChips(s: RunsSearch): FilterChip[] {
  const out: FilterChip[] = []
  if (s.tool) out.push({ key: "tool", label: `tool: ${s.tool}` })
  if (s.model) out.push({ key: "model", label: `model: ${s.model}` })
  if (s.error) out.push({ key: "error", label: `error: ${s.error}` })
  if (s.min_cost !== undefined)
    out.push({ key: "min_cost", label: `cost ≥ ${usd(s.min_cost)}` })
  if (s.min_tokens !== undefined)
    out.push({
      key: "min_tokens",
      label: `tokens ≥ ${s.min_tokens.toLocaleString("en-US")}`,
    })
  if (s.tag) out.push({ key: "tag", label: `tag: ${s.tag}` })
  if (s.session) out.push({ key: "session", label: `session: ${s.session}` })
  if (s.sub) out.push({ key: "sub", label: "with subagent runs" })
  return out
}

function Options({
  label,
  values,
  onPick,
}: {
  label: string
  values: readonly string[]
  onPick: (v: string) => void
}) {
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>{label}</DropdownMenuSubTrigger>
      <DropdownMenuSubContent className="max-h-80 overflow-y-auto">
        {values.length === 0 ? (
          <DropdownMenuItem disabled>None recorded</DropdownMenuItem>
        ) : (
          values.map((v) => (
            <DropdownMenuItem
              key={v}
              className="font-mono text-xs"
              onSelect={() => onPick(v)}
            >
              {v}
            </DropdownMenuItem>
          ))
        )}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}

export function RunsFilterMenu({
  search,
  set,
  tools,
  models,
  errors,
}: {
  search: RunsSearch
  set: (patch: Partial<RunsSearch>) => void
  tools: readonly string[]
  models: readonly string[]
  errors: readonly string[]
}) {
  const [editing, setEditing] = React.useState<TextKey | null>(null)
  const [text, setText] = React.useState("")
  const chips = filterChips(search)
  const patch = editing ? parseFilter(editing, text) : undefined

  const open = (key: TextKey) => {
    setText("")
    setEditing(key)
  }
  const commit = () => {
    if (!patch) return
    set(patch)
    setEditing(null)
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" className="flex-none text-muted-foreground">
            <PlusIcon />
            Filter
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="w-48">
          <Options
            label="Tool"
            values={tools}
            onPick={(tool) => set({ tool })}
          />
          <Options
            label="Model"
            values={models}
            onPick={(model) => set({ model })}
          />
          <Options
            label="Error kind"
            values={errors}
            onPick={(error) => set({ error })}
          />
          <DropdownMenuSeparator />
          {(Object.keys(TEXT) as TextKey[]).map((key) => (
            <DropdownMenuItem key={key} onSelect={() => open(key)}>
              {TEXT[key].menu}
            </DropdownMenuItem>
          ))}
          <DropdownMenuSeparator />
          <DropdownMenuCheckboxItem
            checked={search.sub === true}
            onCheckedChange={(on) => set({ sub: on ? true : undefined })}
          >
            Subagent runs
          </DropdownMenuCheckboxItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {editing && (
        <form
          onSubmit={(e) => {
            e.preventDefault()
            commit()
          }}
          className="flex h-7 flex-none items-center gap-1.5 rounded-input border border-ring bg-input-background pr-1 pl-2 shadow-focus"
        >
          <label
            htmlFor="runs-filter-value"
            className="text-xs text-muted-foreground"
          >
            {TEXT[editing].label}
          </label>
          <input
            id="runs-filter-value"
            autoFocus
            value={text}
            inputMode={TEXT[editing].numeric ? "decimal" : undefined}
            placeholder={TEXT[editing].placeholder}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setEditing(null)
            }}
            onBlur={() => (patch ? commit() : setEditing(null))}
            aria-invalid={text.trim() !== "" && !patch}
            className="w-32 bg-transparent font-mono text-xs outline-none placeholder:text-subtle-foreground aria-invalid:text-danger-foreground"
          />
          <button
            type="submit"
            disabled={!patch}
            className="rounded-sm px-1 text-xs font-medium text-foreground disabled:text-subtle-foreground"
          >
            Add
          </button>
        </form>
      )}
      {chips.map((c) => (
        <span
          key={c.key}
          className="inline-flex h-6 max-w-64 flex-none items-center gap-1 rounded-sm bg-muted pr-0.5 pl-2 text-xs"
        >
          <span className="truncate">{c.label}</span>
          <button
            type="button"
            aria-label={`Remove filter ${c.label}`}
            onClick={() => set({ [c.key]: undefined })}
            className="flex size-5 flex-none items-center justify-center rounded-sm text-subtle-foreground outline-none hover:bg-background hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
          >
            <XIcon className="size-3" />
          </button>
        </span>
      ))}
    </>
  )
}
