import { SearchIcon, XIcon } from "lucide-react"
import * as React from "react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"

const ANY = "__any"

/** FilterSelect is one URL filter: "Any <noun>" clears it. */
export function FilterSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: string | undefined
  options: readonly string[]
  onChange: (v: string | undefined) => void
}) {
  return (
    <Select
      value={value ?? ANY}
      onValueChange={(v) => onChange(v === ANY ? undefined : v)}
    >
      <SelectTrigger
        size="sm"
        aria-label={label}
        className={cn(
          "min-w-[132px]",
          value && "border-ring/60 text-foreground"
        )}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ANY}>Any {label.toLowerCase()}</SelectItem>
        {options.map((o) => (
          <SelectItem key={o} value={o}>
            {o}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** SearchFilter writes its text to the URL a moment after typing stops. */
export function SearchFilter({
  value,
  onChange,
  placeholder,
}: {
  value: string | undefined
  onChange: (v: string | undefined) => void
  placeholder: string
}) {
  const [text, setText] = React.useState(value ?? "")
  React.useEffect(() => setText(value ?? ""), [value])
  React.useEffect(() => {
    const t = setTimeout(() => {
      const next = text.trim() || undefined
      if (next !== value) onChange(next)
    }, 250)
    return () => clearTimeout(t)
  }, [text, value, onChange])
  return (
    <div className="relative w-56 max-[760px]:w-full">
      <SearchIcon className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-subtle-foreground" />
      <Input
        type="search"
        aria-label={placeholder}
        placeholder={placeholder}
        value={text}
        onChange={(e) => setText(e.target.value)}
        className="h-8 pl-9"
      />
    </div>
  )
}

export function ClearFilters({
  onClear,
  shown,
}: {
  onClear: () => void
  shown: boolean
}) {
  if (!shown) return null
  return (
    <Button
      variant="ghost"
      size="sm"
      className="h-8 px-2.5 text-muted-foreground"
      onClick={onClear}
    >
      <XIcon />
      Clear filters
    </Button>
  )
}
