import {
  ActivityIcon,
  AlignLeftIcon,
  BotIcon,
  LayoutGridIcon,
  MessageSquareIcon,
  ScrollTextIcon,
  ShieldCheckIcon,
  SlidersHorizontalIcon,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

// The sidebar's information architecture (WEFTBASE-ARCHITECTURE §7.2):
// Overview, Runs, Sessions, Approvals, Agents, Traces, Audit; Settings
// pinned to the bottom. The command menu lists the same pages.
export interface NavItem {
  to:
    | "/"
    | "/runs"
    | "/sessions"
    | "/approvals"
    | "/agents"
    | "/traces"
    | "/audit"
    | "/settings"
  label: string
  icon: LucideIcon
}

export const NAV: NavItem[] = [
  { to: "/", label: "Overview", icon: LayoutGridIcon },
  { to: "/runs", label: "Runs", icon: AlignLeftIcon },
  { to: "/sessions", label: "Sessions", icon: MessageSquareIcon },
  { to: "/approvals", label: "Approvals", icon: ShieldCheckIcon },
  { to: "/agents", label: "Agents", icon: BotIcon },
  { to: "/traces", label: "Traces", icon: ActivityIcon },
  { to: "/audit", label: "Audit", icon: ScrollTextIcon },
]

export const SETTINGS_NAV: NavItem = {
  to: "/settings",
  label: "Settings",
  icon: SlidersHorizontalIcon,
}
