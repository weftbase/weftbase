import * as React from "react"
import { CheckIcon, CopyIcon } from "lucide-react"

import { cn } from "@/lib/utils"

// CodeBlock (design system): always dark, in both themes. Header with
// three dots, the name in 12px mono and a copy button; body 14px mono,
// scrolling sideways instead of wrapping.
export function CodeBlock({
  title,
  code,
  className,
}: {
  title: string
  code: string
  className?: string
}) {
  const [copied, setCopied] = React.useState(false)
  const copy = () => {
    void navigator.clipboard?.writeText(code).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    })
  }
  return (
    <div className={cn("overflow-hidden code-block", className)}>
      <div className="flex items-center gap-3 border-b border-code-border px-4 py-2.5">
        <span aria-hidden className="flex gap-1.5">
          <span className="size-2.5 rounded-full bg-code-dot" />
          <span className="size-2.5 rounded-full bg-code-dot" />
          <span className="size-2.5 rounded-full bg-code-dot" />
        </span>
        <span className="truncate font-mono text-xs text-code-muted">
          {title}
        </span>
        <button
          type="button"
          onClick={copy}
          aria-label={copied ? "Copied" : `Copy ${title}`}
          className="ml-auto inline-flex size-7 items-center justify-center rounded-md text-code-muted hover:bg-code-border hover:text-code-foreground"
        >
          {copied ? (
            <CheckIcon className="size-[15px] text-code-success" />
          ) : (
            <CopyIcon className="size-[15px]" />
          )}
        </button>
      </div>
      <pre className="overflow-x-auto px-5 py-4 font-mono text-[13px] leading-[1.75]">
        <code>{code}</code>
      </pre>
    </div>
  )
}
