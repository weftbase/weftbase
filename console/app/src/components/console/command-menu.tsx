import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import {
  AlignLeftIcon,
  BotIcon,
  KeyboardIcon,
  MessageSquareIcon,
  MonitorIcon,
  MoonIcon,
  PlayIcon,
  SearchIcon,
  SunIcon,
} from "lucide-react"
import * as React from "react"

import { NAV, SETTINGS_NAV } from "@/components/console/nav"
import { StatusDot } from "@/components/console/status"
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command"
import { api } from "@/lib/api/http"
import { q } from "@/lib/api/queries"
import { shortId } from "@/lib/format"
import { exactFirst, idCandidate, matches } from "@/lib/jump"
import { rowCause, rowStatus } from "@/lib/runs"
import { setTheme } from "@/lib/theme"

/** What typed text resolves to as an id: runs and sessions whose id is,
 * or starts with, it; else the session a public id stands for. */
function useJump(id: string | undefined) {
  const on = id !== undefined
  const key = id ?? ""
  const runs = useQuery({
    queryKey: ["jump", "runs", key],
    queryFn: () => api.findRuns(key),
    enabled: on,
    retry: false,
  })
  // By prefix, like the runs: a list answers "none" without a 404.
  const sessions = useQuery({
    queryKey: ["jump", "sessions", key],
    queryFn: () => api.sessions({ id_prefix: key, limit: 3 }),
    enabled: on,
    retry: false,
  })
  const none = runs.data?.length === 0 && sessions.data?.sessions.length === 0
  const publicId = useQuery({
    queryKey: ["jump", "public", key],
    queryFn: () => api.publicSession(key),
    enabled: on && none,
    retry: false,
  })
  return {
    runs: on ? exactFirst(runs.data ?? [], key) : [],
    sessions: on ? (sessions.data?.sessions ?? []) : [],
    publicSession: on ? publicId.data?.session_id : undefined,
  }
}

/** useDebounced lags a value, so a lookup waits for the typing to stop. */
function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = React.useState(value)
  React.useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

// The ⌘K command menu: pages, New run, agents; an exact run, session or
// public id jumps straight there; anything else searches runs. Every
// target is a URL.
export function CommandMenu({
  open,
  onOpenChange,
  onNewRun,
  onShortcuts,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** Open the "New run" dialog. */
  onNewRun: () => void
  /** Open the keyboard shortcuts sheet. */
  onShortcuts: () => void
}) {
  const navigate = useNavigate()
  const [text, setText] = React.useState("")
  const typed = text.trim()
  const agents = useQuery({ ...q.agentsSummary(), enabled: open })
  const jump = useJump(idCandidate(useDebounced(typed, 200)))

  const go = (fn: () => void) => {
    onOpenChange(false)
    setText("")
    fn()
  }
  const openRun = (runId: string) =>
    go(() => void navigate({ to: "/runs/$runId", params: { runId } }))
  const openSession = (sessionId: string) =>
    go(
      () => void navigate({ to: "/sessions/$sessionId", params: { sessionId } })
    )

  const pages = [...NAV, SETTINGS_NAV].filter((p) =>
    matches(typed, p.label, "go to page")
  )
  const newRun = matches(typed, "New run", "start")
  const agentRows = (agents.data?.agents ?? []).filter((a) =>
    matches(typed, a.name, "agent")
  )
  const actions = [
    {
      label: "Keyboard shortcuts",
      icon: KeyboardIcon,
      words: "keyboard shortcuts help keys",
      run: () => go(onShortcuts),
    },
    {
      label: "Use the system theme",
      icon: MonitorIcon,
      words: "theme system",
      run: () => go(() => setTheme("system")),
    },
    {
      label: "Switch to light",
      icon: SunIcon,
      words: "theme light",
      run: () => go(() => setTheme("light")),
    },
    {
      label: "Switch to dark",
      icon: MoonIcon,
      words: "theme dark",
      run: () => go(() => setTheme("dark")),
    },
  ].filter((a) => matches(typed, a.label, a.words))
  const found =
    jump.runs.length > 0 || jump.sessions.length > 0 || jump.publicSession
  // The highlighted row. cmdk keeps the highlight on the row it was on,
  // so when an id lookup answers, move it to the first hit: Enter then
  // jumps straight there instead of searching.
  const [selected, setSelected] = React.useState("")
  const firstHit = jump.runs.at(0)
    ? `jump run ${jump.runs[0].id}`.toLowerCase()
    : jump.sessions.at(0)
      ? `jump session ${jump.sessions[0].id}`.toLowerCase()
      : jump.publicSession
        ? `jump public ${jump.publicSession}`.toLowerCase()
        : undefined
  React.useEffect(() => {
    if (firstHit) setSelected(firstHit)
  }, [firstHit])
  // A closed menu forgets what was typed.
  React.useEffect(() => {
    if (!open) setText("")
  }, [open])

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      description="Go to a page or an agent, jump to a run or session by id, or search runs."
    >
      {/* Filtering is done here: an id lookup's answers must not be
          filtered away by the text that found them. */}
      <Command
        shouldFilter={false}
        value={selected}
        onValueChange={setSelected}
      >
        <CommandInput
          value={text}
          onValueChange={setText}
          placeholder="Go to a page or agent, paste an id, or search runs"
        />
        <CommandList>
          <CommandEmpty>Nothing matches.</CommandEmpty>
          {found && (
            <CommandGroup heading="Jump to">
              {jump.runs.map((r) => (
                <CommandItem
                  key={r.id}
                  value={`jump run ${r.id}`.toLowerCase()}
                  onSelect={() => openRun(r.id)}
                >
                  <AlignLeftIcon />
                  <span className="font-medium">Run</span>
                  <span className="font-medium">{r.agent}</span>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {r.prompt_preview}
                  </span>
                  <span className="font-mono text-xs text-subtle-foreground">
                    {shortId(r.id, 10)}
                  </span>
                  <StatusDot state={rowStatus(r)} cause={rowCause(r)} />
                </CommandItem>
              ))}
              {jump.sessions.map((ses) => (
                <CommandItem
                  key={ses.id}
                  value={`jump session ${ses.id}`.toLowerCase()}
                  onSelect={() => openSession(ses.id)}
                >
                  <MessageSquareIcon />
                  <span className="font-medium">Session</span>
                  <span className="font-medium">{ses.agent}</span>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {ses.title ?? ses.first_prompt}
                  </span>
                  <span className="font-mono text-xs text-subtle-foreground">
                    {shortId(ses.id, 10)}
                  </span>
                </CommandItem>
              ))}
              {jump.publicSession && (
                <CommandItem
                  value={`jump public ${jump.publicSession}`.toLowerCase()}
                  onSelect={() => openSession(jump.publicSession!)}
                >
                  <MessageSquareIcon />
                  <span className="font-medium">Session</span>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    with public id {typed}
                  </span>
                </CommandItem>
              )}
            </CommandGroup>
          )}
          {(pages.length > 0 || newRun) && (
            <CommandGroup heading="Go to">
              {pages.map((item) => (
                <CommandItem
                  key={item.to}
                  value={`page ${item.label}`}
                  onSelect={() => go(() => void navigate({ to: item.to }))}
                >
                  <item.icon />
                  {item.label}
                </CommandItem>
              ))}
              {newRun && (
                <CommandItem
                  value="action new run"
                  onSelect={() => go(onNewRun)}
                >
                  <PlayIcon />
                  New run
                </CommandItem>
              )}
            </CommandGroup>
          )}
          {agentRows.length > 0 && (
            <CommandGroup heading="Agents">
              {agentRows.map((a) => (
                <CommandItem
                  key={a.name}
                  value={`agent ${a.name}`}
                  onSelect={() =>
                    go(
                      () =>
                        void navigate({
                          to: "/agents/$name",
                          params: { name: a.name },
                        })
                    )
                  }
                >
                  <BotIcon />
                  <span className="font-medium">{a.name}</span>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {a.registered
                      ? [a.summary.model.name, a.summary.placement]
                          .filter(Boolean)
                          .join(" · ")
                      : "not registered"}
                  </span>
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          {actions.length > 0 && (
            <CommandGroup heading="Actions">
              {actions.map((a) => (
                <CommandItem
                  key={a.label}
                  value={`action ${a.label}`}
                  onSelect={a.run}
                >
                  <a.icon />
                  {a.label}
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          {typed !== "" && (
            <CommandGroup heading="Search">
              <CommandItem
                value={`search runs ${typed}`}
                onSelect={() =>
                  go(() => void navigate({ to: "/runs", search: { q: typed } }))
                }
              >
                <SearchIcon />
                <span className="min-w-0 truncate">
                  Search runs for “{typed}”
                </span>
              </CommandItem>
            </CommandGroup>
          )}
        </CommandList>
      </Command>
    </CommandDialog>
  )
}
