import { useQuery } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import {
  AlignLeftIcon,
  BotIcon,
  MessageSquareIcon,
  MonitorIcon,
  MoonIcon,
  SunIcon,
} from "lucide-react"

import { NAV, SETTINGS_NAV } from "@/components/console/nav"
import { RunStatusBadge } from "@/components/console/status"
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from "@/components/ui/command"
import { q } from "@/lib/api/queries"
import { useEnv } from "@/lib/search"
import { setTheme } from "@/lib/theme"

// The ⌘K command menu: go to a page, open a recent run, agent or session,
// or change the theme. Every target is a URL.
export function CommandMenu({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const env = useEnv()
  const navigate = useNavigate()
  const runs = useQuery({ ...q.runs(env, { limit: 8 }), enabled: open })
  const agents = useQuery({ ...q.agents(env), enabled: open })
  const sessions = useQuery({ ...q.sessions(env, {}), enabled: open })

  const go = (fn: () => void) => {
    onOpenChange(false)
    fn()
  }

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange}>
      <Command>
        <CommandInput placeholder="Search runs, sessions, agents" />
        <CommandList>
          <CommandEmpty>
            Nothing matches. Try a run id or an agent name.
          </CommandEmpty>
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
          <CommandSeparator />
          <CommandGroup heading="Recent runs">
            {runs.data?.items.map((r) => (
              <CommandItem
                key={r.id}
                value={`run ${r.id} ${r.agent}`}
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
                <span className="font-mono text-[13px]">{r.id}</span>
                <span className="truncate text-muted-foreground">
                  {r.agent}
                </span>
                <span className="ml-auto">
                  <RunStatusBadge status={r.status} short />
                </span>
              </CommandItem>
            ))}
          </CommandGroup>
          <CommandGroup heading="Agents">
            {agents.data?.map((a) => (
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
                {a.name}
              </CommandItem>
            ))}
          </CommandGroup>
          <CommandGroup heading="Sessions">
            {sessions.data?.items.slice(0, 5).map((s) => (
              <CommandItem
                key={s.id}
                value={`session ${s.id} ${s.agent}`}
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
                <span className="font-mono text-[13px]">{s.id}</span>
                <span className="truncate text-muted-foreground">
                  {s.agent}
                </span>
              </CommandItem>
            ))}
          </CommandGroup>
          <CommandSeparator />
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
