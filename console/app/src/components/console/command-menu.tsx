import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import {
  AlignLeftIcon,
  MessageSquareIcon,
  MonitorIcon,
  MoonIcon,
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
import { q } from "@/lib/api/queries"
import { shortId } from "@/lib/format"
import { displayStatus, sessionStatus } from "@/lib/api/status"
import { setTheme } from "@/lib/theme"

// The ⌘K command menu: go to a page, open a recent run or session,
// search runs for what was typed, or change the theme. Every target is a
// URL.
export function CommandMenu({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const navigate = useNavigate()
  const [text, setText] = React.useState("")
  const runs = useQuery({ ...q.runsPage({ limit: 6 }), enabled: open })
  const sessions = useQuery({ ...q.sessions({ limit: 5 }), enabled: open })

  const go = (fn: () => void) => {
    onOpenChange(false)
    setText("")
    fn()
  }

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange}>
      <Command>
        <CommandInput
          value={text}
          onValueChange={setText}
          placeholder="Go to a page, a run or a session"
        />
        <CommandList>
          <CommandEmpty>Nothing matches.</CommandEmpty>
          {text.trim() !== "" && (
            <CommandGroup heading="Search">
              <CommandItem
                value={`search runs ${text}`}
                onSelect={() =>
                  go(
                    () =>
                      void navigate({ to: "/runs", search: { q: text.trim() } })
                  )
                }
              >
                <SearchIcon />
                Search runs for “{text.trim()}”
              </CommandItem>
            </CommandGroup>
          )}
          <CommandGroup heading="Go to">
            {[...NAV, SETTINGS_NAV].map((item) => (
              <CommandItem
                key={item.to}
                value={`page ${item.label}`}
                onSelect={() => go(() => void navigate({ to: item.to }))}
              >
                <item.icon />
                {item.label}
              </CommandItem>
            ))}
          </CommandGroup>
          {runs.data && runs.data.runs.length > 0 && (
            <CommandGroup heading="Latest runs">
              {runs.data.runs.map((r) => (
                <CommandItem
                  key={r.id}
                  value={`run ${r.id} ${r.agent} ${r.prompt_preview ?? ""}`}
                  onSelect={() =>
                    go(
                      () =>
                        void navigate({
                          to: "/runs/$runId",
                          params: { runId: r.id },
                        })
                    )
                  }
                >
                  <AlignLeftIcon />
                  <span className="font-medium">{r.agent}</span>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {r.prompt_preview}
                  </span>
                  <span className="font-mono text-xs text-subtle-foreground">
                    {shortId(r.id, 6)}
                  </span>
                  <StatusDot state={displayStatus(r)} />
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          {sessions.data && sessions.data.sessions.length > 0 && (
            <CommandGroup heading="Latest sessions">
              {sessions.data.sessions.map((s) => (
                <CommandItem
                  key={s.id}
                  value={`session ${s.id} ${s.agent} ${s.title ?? s.first_prompt ?? ""}`}
                  onSelect={() =>
                    go(
                      () =>
                        void navigate({
                          to: "/sessions/$sessionId",
                          params: { sessionId: s.id },
                        })
                    )
                  }
                >
                  <MessageSquareIcon />
                  <span className="font-medium">{s.agent}</span>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {s.title ?? s.first_prompt}
                  </span>
                  <StatusDot state={sessionStatus(s)} />
                </CommandItem>
              ))}
            </CommandGroup>
          )}
          <CommandGroup heading="Theme">
            <CommandItem
              value="theme system"
              onSelect={() => go(() => setTheme("system"))}
            >
              <MonitorIcon />
              Use the system theme
            </CommandItem>
            <CommandItem
              value="theme light"
              onSelect={() => go(() => setTheme("light"))}
            >
              <SunIcon />
              Switch to light
            </CommandItem>
            <CommandItem
              value="theme dark"
              onSelect={() => go(() => setTheme("dark"))}
            >
              <MoonIcon />
              Switch to dark
            </CommandItem>
          </CommandGroup>
        </CommandList>
      </Command>
    </CommandDialog>
  )
}
