import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, Outlet } from "@tanstack/react-router"
import {
  CheckIcon,
  LogOutIcon,
  MonitorIcon,
  MoonIcon,
  PanelLeftIcon,
  SearchIcon,
  SunIcon,
  UserIcon,
} from "lucide-react"
import * as React from "react"

import { CommandMenu } from "@/components/console/command-menu"
import { NAV, SETTINGS_NAV } from "@/components/console/nav"
import { NewRun } from "@/components/console/new-run"
import type { NavItem } from "@/components/console/nav"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Kbd } from "@/components/ui/kbd"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { WeftbaseBrand } from "@/components/ui/weftbase-logo"
import { api } from "@/lib/api/http"
import { useRunsLive } from "@/lib/api/live"
import { q } from "@/lib/api/queries"
import type { StreamState } from "@/lib/api/stream"
import type { Meta } from "@/lib/api/types"
import { usePref } from "@/lib/prefs"
import { setTheme, useTheme } from "@/lib/theme"
import type { ThemePref } from "@/lib/theme"
import { cn } from "@/lib/utils"

// The Console shell (CONSOLE-UX, Density): a 200px sidebar that collapses
// to a 44px icon rail (remembered), then a column with the 40px top bar
// and the page. Pages own their padding (components/console/page-header).
// Under 760px the sidebar is always the rail.

const LiveContext = React.createContext<StreamState>("connecting")
/** The state of the project-wide runs stream the shell keeps open. */
export const useLiveState = () => React.useContext(LiveContext)

function NavLink({
  item,
  rail,
  count,
}: {
  item: NavItem
  rail: boolean
  count?: number
}) {
  const Icon = item.icon
  const link = (
    <Link
      to={item.to}
      activeOptions={{ exact: item.to === "/" }}
      aria-label={rail ? item.label : undefined}
      className={cn(
        "relative flex h-7 items-center gap-2 rounded-input text-sm font-medium text-muted-foreground outline-none hover:bg-sidebar-accent hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring aria-[current=page]:bg-sidebar-accent aria-[current=page]:text-foreground aria-[current=page]:shadow-nav-active",
        rail ? "w-7 justify-center" : "px-2"
      )}
    >
      <Icon className="size-4 flex-none" />
      {!rail && item.label}
      {count !== undefined &&
        count > 0 &&
        (rail ? (
          <span
            aria-label={`${count} waiting`}
            className="absolute top-0.5 right-0.5 size-1.5 rounded-full bg-warning"
          />
        ) : (
          <span className="ml-auto count-badge" aria-label={`${count} waiting`}>
            {count}
          </span>
        ))}
    </Link>
  )
  if (!rail) return link
  return (
    <Tooltip>
      <TooltipTrigger asChild>{link}</TooltipTrigger>
      <TooltipContent side="right" sideOffset={6}>
        {item.label}
        {count ? ` · ${count} waiting` : ""}
      </TooltipContent>
    </Tooltip>
  )
}

function Sidebar({ rail }: { rail: boolean }) {
  // Runs waiting on a person: the Approvals count.
  const waiting = useQuery(q.runsPage({ status: "parked", limit: 1 }))
  return (
    <nav
      aria-label="Main"
      className={cn(
        "flex flex-none flex-col border-r border-sidebar-border bg-sidebar",
        rail ? "w-11" : "w-[200px] max-[760px]:w-11"
      )}
    >
      <Link
        to="/"
        aria-label="Weftbase overview"
        className={cn(
          "flex h-10 flex-none items-center border-b border-sidebar-border outline-none focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
          rail
            ? "justify-center"
            : "px-3 max-[760px]:justify-center max-[760px]:px-0"
        )}
      >
        <WeftbaseBrand
          markOnly={rail}
          className="max-[760px]:[&>span:last-child]:hidden"
        />
      </Link>
      <div
        className={cn(
          "flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto py-2",
          rail
            ? "items-center px-0"
            : "px-2 max-[760px]:items-center max-[760px]:px-0"
        )}
      >
        {NAV.map((item) => (
          <ResponsiveNavLink
            key={item.to}
            item={item}
            rail={rail}
            count={item.to === "/approvals" ? waiting.data?.total : undefined}
          />
        ))}
        <div className="mt-auto flex flex-col gap-0.5">
          <ResponsiveNavLink item={SETTINGS_NAV} rail={rail} />
        </div>
      </div>
    </nav>
  )
}

/** Under 760px the sidebar is the rail whatever the preference says. */
function ResponsiveNavLink(props: React.ComponentProps<typeof NavLink>) {
  const narrow = useNarrow()
  return <NavLink {...props} rail={props.rail || narrow} />
}

function useNarrow(): boolean {
  return React.useSyncExternalStore(
    (l) => {
      const mq = matchMedia("(max-width: 760px)")
      mq.addEventListener("change", l)
      return () => mq.removeEventListener("change", l)
    },
    () => matchMedia("(max-width: 760px)").matches,
    () => false
  )
}

const THEMES: { pref: ThemePref; label: string; icon: typeof SunIcon }[] = [
  { pref: "system", label: "System", icon: MonitorIcon },
  { pref: "light", label: "Light", icon: SunIcon },
  { pref: "dark", label: "Dark", icon: MoonIcon },
]

function ThemeToggle() {
  const { pref, dark } = useTheme()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Change theme">
          {dark ? <MoonIcon /> : <SunIcon />}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-36">
        {THEMES.map((t) => (
          <DropdownMenuItem key={t.pref} onSelect={() => setTheme(t.pref)}>
            <t.icon className="size-3.5" />
            {t.label}
            {t.pref === pref && <CheckIcon className="ml-auto size-3.5" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

const PRINCIPAL: Record<Meta["principal"]["kind"], string> = {
  operator: "Signed in with a sign-in link",
  key: "Authenticated with an API key",
  local: "Local access, no sign-in needed",
}

function AccountMenu({ meta }: { meta: Meta }) {
  const qc = useQueryClient()
  const p = meta.principal
  const signOut = async () => {
    try {
      await api.signOut()
    } finally {
      // Whatever the answer, drop what this session loaded and ask the
      // server who we are now.
      qc.removeQueries({ predicate: (query) => query.queryKey[0] !== "meta" })
      void qc.invalidateQueries({ queryKey: ["meta"] })
    }
  }
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Account">
          <UserIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <DropdownMenuLabel className="flex flex-col gap-0.5 font-normal">
          <span className="text-sm font-medium text-foreground">
            {p.kind === "operator"
              ? "Operator"
              : p.kind === "key"
                ? "API key"
                : "Local"}
          </span>
          <span>{PRINCIPAL[p.kind]}</span>
          <span className="truncate font-mono">{p.id}</span>
        </DropdownMenuLabel>
        {p.kind === "operator" && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => void signOut()}>
              <LogOutIcon className="size-3.5" />
              Sign out
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/** The project and environment this session is scoped to. The server
 * fixes both per credential, so this names them; it is not a switch. */
function ScopeChip({ meta }: { meta: Meta }) {
  const { project, env, tenant } = meta.principal.scope
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          tabIndex={0}
          className="inline-flex h-7 flex-none items-center gap-1.5 rounded-input border px-2 text-sm outline-none focus-visible:outline-2 focus-visible:outline-ring"
        >
          <span className="max-w-40 truncate font-medium">{project}</span>
          <span className="text-subtle-foreground">/</span>
          <span
            aria-hidden
            className={cn(
              "size-1.5 rounded-full",
              env === "live" ? "bg-success" : "bg-neutral"
            )}
          />
          <span className="text-muted-foreground">{env}</span>
        </span>
      </TooltipTrigger>
      <TooltipContent side="bottom" sideOffset={6}>
        Project {project}, {env} environment
        {meta.principal.all_tenants ? ", all tenants" : `, tenant ${tenant}`}
      </TooltipContent>
    </Tooltip>
  )
}

function TopBar({
  meta,
  onSearch,
  onToggleSidebar,
}: {
  meta: Meta
  onSearch: () => void
  onToggleSidebar: () => void
}) {
  return (
    <header className="flex h-10 flex-none items-center gap-2 border-b px-2">
      <Button
        variant="ghost"
        size="icon"
        aria-label="Toggle sidebar"
        title="Toggle sidebar (⌘B)"
        onClick={onToggleSidebar}
        className="max-[760px]:hidden"
      >
        <PanelLeftIcon />
      </Button>
      <ScopeChip meta={meta} />
      <button
        type="button"
        onClick={onSearch}
        className="flex h-7 w-72 max-w-full min-w-0 items-center gap-1.5 rounded-input border border-input bg-input-background px-2 text-left text-sm text-subtle-foreground shadow-input outline-none hover:border-ring/40 focus-visible:border-ring focus-visible:shadow-focus"
      >
        <SearchIcon className="size-3.5 flex-none" />
        <span className="flex-1 truncate">Search or jump to</span>
        <Kbd size="sm">⌘K</Kbd>
      </button>
      <div className="ml-auto flex items-center gap-1">
        <NewRun />
        <ThemeToggle />
        <AccountMenu meta={meta} />
      </div>
    </header>
  )
}

export function AppShell({ meta }: { meta: Meta }) {
  const [searchOpen, setSearchOpen] = React.useState(false)
  const [sidebar, setSidebar] = usePref("wb.sidebar", "full")
  const rail = sidebar === "rail"
  const toggle = React.useEffectEvent(() => setSidebar(rail ? null : "rail"))
  const live = useRunsLive()

  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey)) return
      const key = e.key.toLowerCase()
      if (key === "k") {
        e.preventDefault()
        setSearchOpen((o) => !o)
      } else if (key === "b") {
        e.preventDefault()
        toggle()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])

  return (
    <LiveContext value={live}>
      <div className="flex h-svh bg-background text-foreground">
        <a
          href="#main"
          className="sr-only focus:not-sr-only focus:absolute focus:top-1 focus:left-1 focus:z-50 focus:rounded-input focus:bg-background focus:px-2 focus:py-1"
        >
          Skip to content
        </a>
        <Sidebar rail={rail} />
        <div className="flex min-w-0 flex-1 flex-col">
          <TopBar
            meta={meta}
            onSearch={() => setSearchOpen(true)}
            onToggleSidebar={() => setSidebar(rail ? null : "rail")}
          />
          <main id="main" className="min-h-0 flex-1 overflow-hidden">
            <Outlet />
          </main>
        </div>
        <CommandMenu open={searchOpen} onOpenChange={setSearchOpen} />
      </div>
    </LiveContext>
  )
}
