import {
  BrainIcon,
  ChevronRightIcon,
  CornerDownRightIcon,
  PaperclipIcon,
  WrenchIcon,
} from "lucide-react"
import * as React from "react"

import { JsonView } from "@/components/console/json-view"
import type { ContentPart, Message } from "@/lib/api/types"
import { bytes } from "@/lib/format"
import { cn } from "@/lib/utils"

// A conversation as a person reads it: who spoke, what they said, and
// each tool call and tool result folded to one line that opens in place.
// Used by the Transcript tab and by a model call's Input and Output.

const ROLE: Record<string, string> = {
  system: "System",
  user: "User",
  assistant: "Assistant",
  tool: "Tool",
}

const str = (v: unknown) => (typeof v === "string" ? v : undefined)

/** parse returns the JSON value of a string that holds an object or an
 * array, else undefined. */
export function parseJson(s: string): unknown {
  const t = s.trim()
  if (!(t.startsWith("{") || t.startsWith("["))) return undefined
  try {
    return JSON.parse(t) as unknown
  } catch {
    return undefined
  }
}

const preview = (v: unknown, max = 96): string => {
  const s = typeof v === "string" ? v : (JSON.stringify(v) ?? "")
  const one = s.replace(/\s+/g, " ")
  return one.length > max ? `${one.slice(0, max)}…` : one
}

/** Value: recorded content, as JSON when it is JSON, else as text. */
export function Value({
  value,
  depth = 2,
  tone,
}: {
  value: unknown
  depth?: number
  tone?: "danger"
}) {
  const parsed = typeof value === "string" ? parseJson(value) : value
  if (parsed !== undefined && parsed !== null && typeof parsed === "object")
    return <JsonView value={parsed} depth={depth} />
  return (
    <div
      className={cn(
        "max-h-80 overflow-auto rounded-input border bg-surface px-2 py-1.5 font-mono text-xs leading-[18px] break-words whitespace-pre-wrap",
        tone === "danger" &&
          "border-danger/30 bg-danger-muted text-danger-foreground"
      )}
    >
      {typeof value === "string" ? value || "(empty)" : JSON.stringify(value)}
    </div>
  )
}

function Line({
  icon: Icon,
  name,
  what,
  summary,
  tone,
  onOpen,
  children,
}: {
  icon: typeof WrenchIcon
  name: string
  what?: string
  summary: string
  tone?: "danger"
  /** A second action: go to the step in the timeline. */
  onOpen?: () => void
  children: React.ReactNode
}) {
  const [open, setOpen] = React.useState(false)
  return (
    <div className="min-w-0 rounded-input border">
      <div className="flex h-7 items-center gap-1.5 pr-1.5 pl-1">
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((o) => !o)}
          className="flex h-6 min-w-0 flex-1 items-center gap-1.5 rounded-sm text-left outline-none focus-visible:outline-2 focus-visible:outline-ring"
        >
          <ChevronRightIcon
            aria-hidden
            className={cn(
              "size-3 flex-none text-subtle-foreground transition-transform",
              open && "rotate-90"
            )}
          />
          <Icon
            aria-hidden
            className={cn(
              "size-3.5 flex-none text-muted-foreground",
              tone === "danger" && "text-danger-foreground"
            )}
          />
          <span
            className={cn(
              "flex-none text-xs font-medium",
              tone === "danger" && "text-danger-foreground"
            )}
          >
            {name}
          </span>
          {/* The icon and the role over the line say call or result;
              only an error is worth a word. */}
          {what && (
            <span
              className={cn(
                "flex-none text-xs",
                tone === "danger" ? "text-danger-foreground" : "sr-only"
              )}
            >
              {what}
            </span>
          )}
          {!open && (
            <span className="min-w-0 truncate font-mono text-xs text-muted-foreground">
              {summary}
            </span>
          )}
        </button>
        {onOpen && (
          <button
            type="button"
            onClick={onOpen}
            className="flex-none rounded-sm px-1 text-xs text-muted-foreground outline-none hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
          >
            Go to step
          </button>
        )}
      </div>
      {open && <div className="border-t p-1.5">{children}</div>}
    </div>
  )
}

function Part({
  part,
  onCall,
}: {
  part: ContentPart
  onCall?: (callId: string, name: string) => void
}) {
  const p = part as Record<string, unknown>
  switch (part.type) {
    case "text": {
      const text = str(p.text) ?? ""
      return text ? (
        <p className="text-sm break-words whitespace-pre-wrap">{text}</p>
      ) : null
    }
    case "tool_call": {
      const id = str(p.id) ?? ""
      const name = str(p.name) ?? "tool"
      return (
        <Line
          icon={WrenchIcon}
          name={name}
          what="call"
          summary={preview(p.args)}
          onOpen={onCall && id ? () => onCall(id, name) : undefined}
        >
          <Value value={p.args} />
        </Line>
      )
    }
    case "tool_result": {
      const name = str(p.name) ?? "tool"
      const isError = p.is_error === true
      return (
        <Line
          icon={CornerDownRightIcon}
          name={name}
          what={isError ? "error" : "result"}
          summary={preview(p.content)}
          tone={isError ? "danger" : undefined}
        >
          <Value value={p.content} tone={isError ? "danger" : undefined} />
        </Line>
      )
    }
    case "reasoning":
    case "thinking": {
      const text = str(p.text) ?? str(p.thinking) ?? ""
      return (
        <Line icon={BrainIcon} name="Reasoning" summary={preview(text)}>
          <Value value={text} />
        </Line>
      )
    }
    case "file":
    case "image":
    case "audio":
    case "document": {
      const size =
        typeof p.size === "number"
          ? p.size
          : typeof p.bytes === "number"
            ? p.bytes
            : undefined
      const media = str(p.media_type) ?? str(p.mime_type) ?? part.type
      return (
        <span className="inline-flex h-5 w-fit items-center gap-1 rounded-sm border px-1.5 text-xs text-muted-foreground">
          <PaperclipIcon className="size-3" />
          {str(p.name) ?? str(p.filename) ?? part.type}
          <span className="text-subtle-foreground">
            {media}
            {size !== undefined && ` · ${bytes(size)}`}
          </span>
        </span>
      )
    }
    default:
      return (
        <Line icon={WrenchIcon} name={part.type} summary={preview(part)}>
          <JsonView value={part} depth={2} />
        </Line>
      )
  }
}

export function MessageView({
  message,
  onCall,
}: {
  message: Message
  onCall?: (callId: string, name: string) => void
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span className="table-head">{ROLE[message.role] ?? message.role}</span>
      {message.content.length === 0 ? (
        <span className="text-xs text-subtle-foreground">(no content)</span>
      ) : (
        message.content.map((part, i) => (
          <Part key={i} part={part} onCall={onCall} />
        ))
      )}
    </div>
  )
}

/** newFrom: where "new since the previous model call" starts: at the
 * last assistant message (its reply, then the tool results that came
 * back). Everything is new on the first call. */
export function newFrom(messages: readonly Message[]): number {
  for (let i = messages.length - 1; i >= 0; i--)
    if (messages[i].role === "assistant") return i
  return 0
}

/** Messages: a list of messages; the first `fold` of them are behind
 * one "N earlier messages" line. */
export function Messages({
  messages,
  fold = 0,
  onCall,
  className,
}: {
  messages: readonly Message[]
  fold?: number
  onCall?: (callId: string, name: string) => void
  className?: string
}) {
  const [all, setAll] = React.useState(false)
  const hidden = all ? 0 : Math.min(fold, messages.length)
  return (
    <div className={cn("flex min-w-0 flex-col gap-3", className)}>
      {fold > 0 && (
        <button
          type="button"
          aria-expanded={all}
          onClick={() => setAll((a) => !a)}
          className="flex h-6 w-fit items-center gap-1 rounded-sm text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
        >
          <ChevronRightIcon
            aria-hidden
            className={cn(
              "size-3 text-subtle-foreground transition-transform",
              all && "rotate-90"
            )}
          />
          {fold} earlier {fold === 1 ? "message" : "messages"}
        </button>
      )}
      {messages.slice(hidden).map((m, i) => (
        <MessageView key={hidden + i} message={m} onCall={onCall} />
      ))}
    </div>
  )
}
