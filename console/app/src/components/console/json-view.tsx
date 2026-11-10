import { CheckIcon, ChevronRightIcon, CopyIcon } from "lucide-react"
import * as React from "react"

import { useCopy } from "@/components/console/copy-id"
import { cn } from "@/lib/utils"

// JsonView: any value as a collapsible tree, with a copy button for the
// whole document. Objects and arrays past `depth` start collapsed; long
// strings wrap. Plain text, no syntax colours beyond muted keys: it has
// to stay readable in both themes and at 12px.
//
//   <JsonView value={span.attrs} depth={2} />
export function JsonView({
  value,
  depth = 2,
  className,
  copyable = true,
}: {
  value: unknown
  /** Levels open at first. */
  depth?: number
  className?: string
  copyable?: boolean
}) {
  const [copied, copy] = useCopy()
  return (
    <div
      className={cn(
        "group/json relative overflow-auto rounded-input border bg-surface p-2 font-mono text-xs leading-[18px]",
        className
      )}
    >
      {copyable && (
        <button
          type="button"
          aria-label="Copy JSON"
          title={copied ? "Copied" : "Copy JSON"}
          onClick={() => copy(JSON.stringify(value, null, 2))}
          className="absolute top-1 right-1 flex size-6 items-center justify-center rounded-sm border bg-background text-muted-foreground opacity-0 outline-none group-hover/json:opacity-100 hover:text-foreground focus-visible:opacity-100 focus-visible:outline-2 focus-visible:outline-ring"
        >
          {copied ? (
            <CheckIcon className="size-3" />
          ) : (
            <CopyIcon className="size-3" />
          )}
        </button>
      )}
      <Node value={value} open={depth} />
    </div>
  )
}

function Node({
  name,
  value,
  open,
  last = true,
}: {
  name?: string
  value: unknown
  open: number
  last?: boolean
}) {
  const key = name !== undefined && (
    <span className="text-muted-foreground">{JSON.stringify(name)}: </span>
  )
  const comma = last ? "" : ","
  if (value === null || typeof value !== "object") {
    return (
      <div className="pl-4 break-words whitespace-pre-wrap">
        {key}
        <span
          className={cn(
            typeof value === "string" ? "text-foreground" : "text-lane-1",
            value === null && "text-subtle-foreground"
          )}
        >
          {value === undefined ? "undefined" : JSON.stringify(value)}
        </span>
        {comma}
      </div>
    )
  }
  return <Branch name={name} value={value} open={open} comma={comma} />
}

function Branch({
  name,
  value,
  open,
  comma,
}: {
  name?: string
  value: object
  open: number
  comma: string
}) {
  const isArray = Array.isArray(value)
  const entries: [string | undefined, unknown][] = isArray
    ? value.map((v) => [undefined, v])
    : Object.entries(value)
  const [expanded, setExpanded] = React.useState(open > 0)
  const [l, r] = isArray ? ["[", "]"] : ["{", "}"]
  const key = name !== undefined && (
    <span className="text-muted-foreground">{JSON.stringify(name)}: </span>
  )
  if (entries.length === 0)
    return (
      <div className="pl-4">
        {key}
        {l}
        {r}
        {comma}
      </div>
    )
  return (
    <div>
      <button
        type="button"
        aria-expanded={expanded}
        onClick={() => setExpanded((e) => !e)}
        className="flex w-full items-center rounded-sm text-left outline-none hover:bg-muted focus-visible:bg-muted"
      >
        <ChevronRightIcon
          aria-hidden
          className={cn(
            "size-3 flex-none text-subtle-foreground transition-transform",
            expanded && "rotate-90"
          )}
        />
        <span className="pl-1">
          {key}
          {l}
          {!expanded && (
            <span className="text-subtle-foreground">
              {" "}
              {entries.length} {isArray ? "items" : "keys"} {r}
              {comma}
            </span>
          )}
        </span>
      </button>
      {expanded && (
        <>
          <div className="ml-1.5 border-l pl-1">
            {entries.map(([k, v], i) => (
              <Node
                key={k ?? i}
                name={k}
                value={v}
                open={open - 1}
                last={i === entries.length - 1}
              />
            ))}
          </div>
          <div className="pl-4">
            {r}
            {comma}
          </div>
        </>
      )}
    </div>
  )
}
