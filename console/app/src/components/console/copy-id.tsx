import { CheckIcon, CopyIcon } from "lucide-react"
import * as React from "react"

import { shortId } from "@/lib/format"
import { cn } from "@/lib/utils"

/** useCopy copies text and reports "copied" for a moment. */
export function useCopy(): [boolean, (text: string) => void] {
  const [copied, setCopied] = React.useState(false)
  const timer = React.useRef<ReturnType<typeof setTimeout>>(undefined)
  React.useEffect(() => () => clearTimeout(timer.current), [])
  const copy = (text: string) => {
    void navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      clearTimeout(timer.current)
      timer.current = setTimeout(() => setCopied(false), 1200)
    })
  }
  return [copied, copy]
}

/** CopyId: an id in mono; click copies the whole id. Long ids show their
 * end (where they differ) unless `full`. */
export function CopyId({
  id,
  full = false,
  label = "id",
  className,
}: {
  id: string
  full?: boolean
  /** What it is, for the accessible name: "run id". */
  label?: string
  className?: string
}) {
  const [copied, copy] = useCopy()
  return (
    <button
      type="button"
      title={copied ? "Copied" : `Copy ${label}: ${id}`}
      aria-label={`Copy ${label} ${id}`}
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        copy(id)
      }}
      className={cn(
        "group/copy inline-flex h-5 max-w-full flex-none items-center gap-1 rounded-sm px-1 font-mono text-xs text-muted-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring",
        className
      )}
    >
      <span className="truncate">{full ? id : shortId(id)}</span>
      {copied ? (
        <CheckIcon className="size-3 flex-none text-success-foreground" />
      ) : (
        <CopyIcon className="size-3 flex-none opacity-0 group-hover/copy:opacity-100 group-focus-visible/copy:opacity-100" />
      )}
    </button>
  )
}
