import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import type { QueryClient } from "@tanstack/react-query"
import {
  Link,
  Outlet,
  useMatch,
  useNavigate,
  useRouter,
  useRouterState,
} from "@tanstack/react-router"
import {
  CheckIcon,
  CopyIcon,
  LinkIcon,
  LogOutIcon,
  MonitorIcon,
  MoonIcon,
  PanelLeftIcon,
  SearchIcon,
  SunIcon,
  UserIcon,
} from "lucide-react"
import * as React from "react"
import { toast } from "sonner"

import { CommandMenu } from "@/components/console/command-menu"
import { useCopy } from "@/components/console/copy-id"
import { NAV, SETTINGS_NAV } from "@/components/console/nav"
import { NewRun } from "@/components/console/new-run"
import type { NavItem } from "@/components/console/nav"
import { ShortcutsSheet } from "@/components/console/shortcuts"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
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
import { chainQuery } from "@/lib/api/chain"
import { ApiError } from "@/lib/api/errors"
import type { ChainView } from "@/lib/api/chain"
import { api } from "@/lib/api/http"
import { useApprovalsLive, useRunsLive } from "@/lib/api/live"
import { q } from "@/lib/api/queries"
import type { StreamState } from "@/lib/api/stream"
import type { Meta } from "@/lib/api/types"
import { usePref } from "@/lib/prefs"
import { dateTime } from "@/lib/format"
import { setTheme, useTheme } from "@/lib/theme"
import type { ThemePref } from "@/lib/theme"
import { cn } from "@/lib/utils"

// The Console shell (CONSOLE-UX, Density): a 200px sidebar that collapses
// to a 44px icon rail (remembered), then a column with the 40px top bar
// and the page. Pages own their padding (components/console/page-header).
// Under 1000px the sidebar is always the rail.

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
  // Tool calls waiting on a person: the Approvals count, kept current by
  // the outbox stream (useApprovalsLive in the shell).
  const waiting = useQuery(q.approvals({ status: "pending", limit: 1 }))
  return (
    <nav
      aria-label="Main"
      className={cn(
        "flex flex-none flex-col border-r border-sidebar-border bg-sidebar",
        rail ? "w-11" : "w-[200px] max-[1000px]:w-11"
      )}
    >
      <Link
        to="/"
        aria-label="Weftbase overview"
        className={cn(
          "flex h-10 flex-none items-center border-b border-sidebar-border outline-none focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-ring",
          rail
            ? "justify-center"
            : "px-3 max-[1000px]:justify-center max-[1000px]:px-0"
        )}
      >
        <WeftbaseBrand
          markOnly={rail}
          className="max-[1000px]:[&>span:last-child]:hidden"
        />
      </Link>
      <div
        className={cn(
          "flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto py-2",
          rail
            ? "items-center px-0"
            : "px-2 max-[1000px]:items-center max-[1000px]:px-0"
        )}
      >
        {NAV.map((item) => (
          <ResponsiveNavLink
            key={item.to}
            item={item}
            rail={rail}
            count={item.to === "/approvals" ? waiting.data?.pending : undefined}
          />
        ))}
        <div className="mt-auto flex flex-col gap-0.5">
          <ResponsiveNavLink item={SETTINGS_NAV} rail={rail} />
        </div>
      </div>
    </nav>
  )
}

/** Under 1000px the sidebar is the rail whatever the preference says. */
function ResponsiveNavLink(props: React.ComponentProps<typeof NavLink>) {
  const narrow = useNarrow()
  return <NavLink {...props} rail={props.rail || narrow} />
}

function useNarrow(): boolean {
  return React.useSyncExternalStore(
    (l) => {
      const mq = matchMedia("(max-width: 1000px)")
      mq.addEventListener("change", l)
      return () => mq.removeEventListener("change", l)
    },
    () => matchMedia("(max-width: 1000px)").matches,
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
  // A fresh one-time sign-in link. It is a credential: it lives in this
  // mutation's state only (never logged, never stored) and is dropped
  // when the dialog closes.
  const [linking, setLinking] = React.useState(false)
  const link = useMutation({
    mutationFn: () => api.signInLink(window.location.origin),
    gcTime: 0,
  })
  return (
    <>
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
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onSelect={() => {
              setLinking(true)
              link.mutate()
            }}
          >
            <LinkIcon className="size-3.5" />
            New sign-in link…
          </DropdownMenuItem>
          {p.kind === "operator" && (
            <DropdownMenuItem onSelect={() => void signOut()}>
              <LogOutIcon className="size-3.5" />
              Sign out
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <Dialog
        open={linking}
        onOpenChange={(open) => {
          setLinking(open)
          if (!open) link.reset()
        }}
      >
        <DialogContent className="sm:max-w-md">
          <SignInLink
            link={link.data}
            error={link.error}
            onDone={() => {
              setLinking(false)
              link.reset()
            }}
          />
        </DialogContent>
      </Dialog>
    </>
  )
}

/** The sign-in link just made, to open the Console in another browser:
 * shown once, with a copy button. */
function SignInLink({
  link,
  error,
  onDone,
}: {
  link: { link: string; expires_at: string } | undefined
  error: unknown
  onDone: () => void
}) {
  const [copied, copy] = useCopy()
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <DialogHeader>
        <DialogTitle>New sign-in link</DialogTitle>
        <DialogDescription>
          Opens the Console in another browser. Works once
          {link ? `, until ${dateTime(link.expires_at)}` : ""}.
        </DialogDescription>
      </DialogHeader>
      {error ? (
        <p role="alert" className="text-xs text-danger-foreground">
          {error instanceof ApiError && error.status === 403
            ? "This credential may not create sign-in links."
            : error instanceof Error
              ? error.message
              : String(error)}
        </p>
      ) : (
        <div className="flex items-center gap-2">
          <output
            aria-label="Sign-in link"
            data-sign-in-link
            className="flex h-7 min-w-0 flex-1 items-center rounded-input border bg-surface px-2 font-mono text-xs"
          >
            <span className="truncate">
              {link?.link ?? "Creating the link…"}
            </span>
          </output>
          <Button
            variant="secondary"
            disabled={!link}
            onClick={() => link && copy(link.link)}
          >
            {copied ? <CheckIcon /> : <CopyIcon />}
            {copied ? "Copied" : "Copy"}
          </Button>
        </div>
      )}
      <DialogFooter>
        <Button variant="ghost" onClick={onDone}>
          Done
        </Button>
      </DialogFooter>
    </div>
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
  newRunOpen,
  onNewRunOpenChange,
}: {
  meta: Meta
  onSearch: () => void
  onToggleSidebar: () => void
  newRunOpen: boolean
  onNewRunOpenChange: (open: boolean) => void
}) {
  // On an agent's page, "New run" starts with that agent chosen.
  const onAgent = useMatch({ from: "/agents/$name", shouldThrow: false })
  return (
    <header className="flex h-10 flex-none items-center gap-2 border-b px-2">
      <Button
        variant="ghost"
        size="icon"
        aria-label="Toggle sidebar"
        title="Toggle sidebar (⌘B)"
        onClick={onToggleSidebar}
        className="max-[1000px]:hidden"
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
        <NewRun
          agent={onAgent?.params.name}
          open={newRunOpen}
          onOpenChange={onNewRunOpenChange}
        />
        <ThemeToggle />
        <AccountMenu meta={meta} />
      </div>
    </header>
  )
}

const APPROVAL_TOAST = "approval-waiting"
/** How long the toast shows, and how long after it no new one is raised. */
const APPROVAL_TOAST_MS = 6000
const APPROVAL_TOAST_QUIET_MS = 5000

/** Whether an approval asked for by `runId` is already in front of the
 * person: the inbox is open, or the page is that run's (the run itself,
 * its parent's, or any run of the chain it continues: a run that parked,
 * resumed and parked again is one run on one page). */
export function approvalOnScreen(
  matches: readonly { routeId: string; params: Record<string, unknown> }[],
  runId: string,
  qc: QueryClient
): boolean {
  for (const m of matches) {
    if (m.routeId === "/approvals/") return true
    if (m.routeId !== "/runs/$runId" || runId === "") continue
    const shown = String(m.params.runId)
    // A subagent's run is `<parent>/<step>/<call>`: its parent's page
    // shows it.
    if (shown === runId || shown === runId.split("/")[0]) return true
    const chain = qc.getQueryData<ChainView>(chainQuery(shown).queryKey)
    if (chain?.runs.some((r) => r.id === runId.split("/")[0])) return true
  }
  return false
}

export function AppShell({ meta }: { meta: Meta }) {
  const [searchOpen, setSearchOpen] = React.useState(false)
  const [sidebar, setSidebar] = usePref("wb.sidebar", "full")
  const rail = sidebar === "rail"
  const toggle = React.useEffectEvent(() => setSidebar(rail ? null : "rail"))
  const live = useRunsLive()
  const [keysOpen, setKeysOpen] = React.useState(false)
  const [newRunOpen, setNewRunOpen] = React.useState(false)
  const navigate = useNavigate()
  const router = useRouter()
  const qc = useQueryClient()
  /** The run the toast on screen (if any) is about. */
  const toastFor = React.useRef("")
  /** Until when the toast shows, and until when no new one is raised. */
  const toastTimes = React.useRef({ until: 0, quietUntil: 0 })
  // The one toast: a tool call began to wait on a person. It is not
  // raised for what is already on screen (the inbox, or that run's own
  // page, which shows the request with Approve / Deny). It goes away by
  // itself. A newer one takes the place of the one showing (same id)
  // without keeping it up longer, and after it has gone the next one
  // waits a little: when approvals come one after another the toast is
  // not there all the time (the Approvals count in the nav is).
  useApprovalsLive((e) => {
    const runId = typeof e.data?.run_id === "string" ? e.data.run_id : ""
    if (approvalOnScreen(router.state.matches, runId, qc)) return
    const now = Date.now()
    const t = toastTimes.current
    const showing = now < t.until
    if (!showing && now < t.quietUntil) return
    if (!showing) t.until = now + APPROVAL_TOAST_MS
    t.quietUntil = t.until + APPROVAL_TOAST_QUIET_MS
    toastFor.current = runId
    toast("A run is waiting for your approval", {
      id: APPROVAL_TOAST,
      duration: t.until - now,
      onDismiss: () => {
        t.until = 0
      },
      description:
        typeof e.data?.agent === "string" && typeof e.data.tool === "string"
          ? `${e.data.agent} wants to call ${e.data.tool}`
          : undefined,
      action: {
        label: "Review",
        onClick: () =>
          void navigate({ to: "/approvals", search: { id: e.resource_id } }),
      },
    })
  })
  // Going to the inbox, or to the run the toast is about, answers it:
  // take it down.
  const page = useRouterState({
    select: (s) => s.matches.map((m) => m.id).join(" "),
  })
  React.useEffect(() => {
    if (approvalOnScreen(router.state.matches, toastFor.current, qc))
      toast.dismiss(APPROVAL_TOAST)
  }, [page, router, qc])

  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null
      if (
        e.key === "?" &&
        !e.metaKey &&
        !e.ctrlKey &&
        !e.altKey &&
        !(
          el &&
          (["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName) ||
            el.isContentEditable)
        )
      ) {
        e.preventDefault()
        setKeysOpen(true)
        return
      }
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
            newRunOpen={newRunOpen}
            onNewRunOpenChange={setNewRunOpen}
          />
          <main id="main" className="min-h-0 flex-1 overflow-hidden">
            <Outlet />
          </main>
        </div>
        <CommandMenu
          open={searchOpen}
          onOpenChange={setSearchOpen}
          onNewRun={() => setNewRunOpen(true)}
          onShortcuts={() => setKeysOpen(true)}
        />
        <ShortcutsSheet open={keysOpen} onOpenChange={setKeysOpen} />
      </div>
    </LiveContext>
  )
}
