import {
  AlignLeftIcon,
  BotIcon,
  LayoutGridIcon,
  MessageSquareIcon,
  ShieldCheckIcon,
  SlidersHorizontalIcon,
} from "lucide-react"
import type { LucideIcon } from "lucide-react"

// The sidebar (CONSOLE-UX, Navigation): Overview, Runs, Sessions,
// Approvals, Agents; Settings pinned to the bottom. Traces are a tab on
// Runs. The command menu lists the same pages.
export interface NavItem {
  to: "/" | "/runs" | "/sessions" | "/approvals" | "/agents" | "/settings"
  label: string
  icon: LucideIcon
}

export const NAV: NavItem[] = [
  { to: "/", label: "Overview", icon: LayoutGridIcon },
  { to: "/runs", label: "Runs", icon: AlignLeftIcon },
  { to: "/sessions", label: "Sessions", icon: MessageSquareIcon },
  { to: "/approvals", label: "Approvals", icon: ShieldCheckIcon },
  { to: "/agents", label: "Agents", icon: BotIcon },
]

export const SETTINGS_NAV: NavItem = {
  to: "/settings",
  label: "Settings",
  icon: SlidersHorizontalIcon,
}
