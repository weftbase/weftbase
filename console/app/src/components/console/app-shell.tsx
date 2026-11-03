import { useQuery } from "@tanstack/react-query"
import { Link, Outlet, useNavigate } from "@tanstack/react-router"
import {
  CheckIcon,
  MenuIcon,
  MonitorIcon,
  MoonIcon,
  PlusIcon,
  SearchIcon,
  SunIcon,
} from "lucide-react"
import * as React from "react"

import { CommandMenu } from "@/components/console/command-menu"
import { NAV, SETTINGS_NAV } from "@/components/console/nav"
import type { NavItem } from "@/components/console/nav"
import { Badge } from "@/components/ui/badge"
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
import { api } from "@/lib/api/client"
import { q } from "@/lib/api/queries"
import type { Env } from "@/lib/api/types"
import { useEnv } from "@/lib/search"
import { setTheme, useTheme } from "@/lib/theme"
import type { ThemePref } from "@/lib/theme"
import { cn } from "@/lib/utils"

// The Console shell (design system, Console layout): a 232px sidebar,
// then a column with the 60px top bar and the page (padding 24px).
// Under 760px the sidebar hides and a menu button takes its place.

const navItemClass =
  "flex h-9 items-center gap-2.5 rounded-md px-2.5 text-sm font-medium text-muted-foreground transition-colors outline-none hover:bg-sidebar-accent hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring aria-[current=page]:bg-sidebar-accent aria-[current=page]:text-foreground aria-[current=page]:shadow-nav-active"

function NavLink({ item, count }: { item: NavItem; count?: number }) {
  const Icon = item.icon
  return (
    <Link
      to={item.to}
      className={navItemClass}
      activeOptions={{ exact: item.to === "/" }}
    >
      <Icon className="size-[17px] flex-none" />
      {item.label}
      {count !== undefined && count > 0 && (
        <span className="ml-auto count-badge" aria-label={`${count} waiting`}>
          {count}
        </span>
      )}
    </Link>
  )
}

function usePendingCount(): number | undefined {
  const env = useEnv()
  const stats = useQuery(q.stats(env))
  return stats.data?.pending_approvals
}

function Sidebar() {
  const pending = usePendingCount()
  const meta = useQuery(q.meta())
  const workspace = meta.data?.workspace ?? "Workspace"
  return (
    <nav
      aria-label="Main"
      className="flex w-[232px] flex-none flex-col gap-1 border-r border-sidebar-border bg-sidebar px-3.5 py-[18px] max-[760px]:hidden"
    >
      <Link
        to="/"
        className="mb-1 rounded-md px-2 pt-1 pb-[18px]"
        aria-label="Weftbase overview"
      >
        <WeftbaseBrand />
      </Link>
      {NAV.map((item) => (
        <NavLink
          key={item.to}
          item={item}
          count={item.to === "/approvals" ? pending : undefined}
        />
      ))}
      <div className="mt-auto flex flex-col gap-1">
        <NavLink item={SETTINGS_NAV} />
        <div className="mt-1.5 flex items-center gap-2.5 border-t border-sidebar-border p-2.5">
          <span
            aria-hidden
            className="flex size-[30px] flex-none items-center justify-center rounded-full border bg-muted text-xs font-semibold text-muted-foreground"
          >
            {initials(workspace)}
          </span>
          <span className="min-w-0">
            <span className="block truncate text-[13px] font-medium">
              {workspace}
            </span>
            <span className="block text-xs text-subtle-foreground">
              self-hosted
            </span>
          </span>
        </div>
      </div>
    </nav>
  )
}

function initials(name: string): string {
  const letters = name
    .replace(/[^A-Za-z ]/g, "")
    .split(" ")
    .filter(Boolean)
  return (
    letters.length >= 2
      ? letters[0][0] + letters[1][0]
      : (letters[0] ?? "W").slice(0, 2)
  ).toUpperCase()
}

function MobileNav() {
  const pending = usePendingCount()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon-sm"
          className="min-[761px]:hidden"
          aria-label="Open navigation"
        >
          <MenuIcon className="size-[18px]" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        {[...NAV, SETTINGS_NAV].map((item) => (
          <DropdownMenuItem key={item.to} asChild>
            <Link to={item.to}>
              <item.icon className="size-4" />
              {item.label}
              {item.to === "/approvals" && pending ? (
                <span className="ml-auto count-badge">{pending}</span>
              ) : null}
            </Link>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

const ENV_LABEL: Record<Env, string> = { live: "live", test: "test" }

function EnvSwitcher() {
  const env = useEnv()
  const navigate = useNavigate()
  const choose = (next: Env) =>
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        env: next === "live" ? undefined : next,
      }),
    })
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className="inline-flex h-8 items-center gap-2 rounded-md border px-3 text-[13px] text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
          aria-label={`Environment: ${ENV_LABEL[env]}. Change environment`}
        >
          <span
            aria-hidden
            className={cn(
              "size-[7px] rounded-full",
              env === "live" ? "bg-success" : "bg-neutral"
            )}
          />
          {ENV_LABEL[env]}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-60">
        <DropdownMenuLabel className="eyebrow">Environment</DropdownMenuLabel>
        {(["live", "test"] as const).map((e) => (
          <DropdownMenuItem key={e} onSelect={() => choose(e)}>
            <span
              aria-hidden
              className={cn(
                "size-[7px] rounded-full",
                e === "live" ? "bg-success" : "bg-neutral"
              )}
            />
            <span className="flex flex-col">
              <span>{ENV_LABEL[e]}</span>
              <span className="text-xs text-subtle-foreground">
                {e === "live"
                  ? "Production keys and traffic"
                  : "Test keys, safe to break"}
              </span>
            </span>
            {e === env && <CheckIcon className="ml-auto size-4" />}
          </DropdownMenuItem>
        ))}
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="text-xs font-normal text-subtle-foreground">
          Filter by tenant on each list page.
        </DropdownMenuLabel>
      </DropdownMenuContent>
    </DropdownMenu>
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
        <Button variant="ghost" size="icon-sm" aria-label="Change theme">
          {dark ? (
            <MoonIcon className="size-4" />
          ) : (
            <SunIcon className="size-4" />
          )}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-40">
        {THEMES.map((t) => (
          <DropdownMenuItem key={t.pref} onSelect={() => setTheme(t.pref)}>
            <t.icon className="size-4" />
            {t.label}
            {t.pref === pref && <CheckIcon className="ml-auto size-4" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function TopBar({ onSearch }: { onSearch: () => void }) {
  return (
    <header className="flex h-15 flex-none items-center gap-4 border-b px-6 max-[760px]:gap-2.5 max-[760px]:px-4">
      <MobileNav />
      <button
        type="button"
        onClick={onSearch}
        className="flex h-9 max-w-[420px] min-w-0 flex-1 items-center gap-2 rounded-input border border-input bg-input-background px-3 text-left text-sm text-subtle-foreground shadow-input outline-none hover:border-ring/40 focus-visible:border-ring focus-visible:shadow-focus"
      >
        <SearchIcon className="size-4 flex-none" />
        <span className="flex-1 truncate">Search runs, sessions, agents</span>
        <Kbd size="sm">⌘K</Kbd>
      </button>
      <div className="ml-auto flex items-center gap-2.5">
        {api.kind === "mock" && (
          <Tooltip>
            <TooltipTrigger asChild>
              <span tabIndex={0} className="max-[1000px]:hidden">
                <Badge
                  variant="outline"
                  size="tag"
                  className="font-mono text-muted-foreground"
                >
                  mock data
                </Badge>
              </span>
            </TooltipTrigger>
            <TooltipContent>
              The /v1 API is not connected. Every number here is sample data.
            </TooltipContent>
          </Tooltip>
        )}
        <EnvSwitcher />
        <ThemeToggle />
        <Tooltip>
          <TooltipTrigger asChild>
            <span tabIndex={0} className="max-[560px]:hidden">
              <Button size="sm" disabled>
                <PlusIcon />
                New run
              </Button>
            </span>
          </TooltipTrigger>
          <TooltipContent>
            Planned: starts a run on an agent label once workers connect.
          </TooltipContent>
        </Tooltip>
      </div>
    </header>
  )
}

export function AppShell() {
  const [searchOpen, setSearchOpen] = React.useState(false)
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault()
        setSearchOpen((o) => !o)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])
  return (
    <div className="flex h-svh bg-background text-foreground">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <TopBar onSearch={() => setSearchOpen(true)} />
        <main
          id="main"
          className="min-h-0 flex-1 overflow-auto p-6 max-[760px]:p-4"
        >
          <Outlet />
        </main>
      </div>
      <CommandMenu open={searchOpen} onOpenChange={setSearchOpen} />
    </div>
  )
}
