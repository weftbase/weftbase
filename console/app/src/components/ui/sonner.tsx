import { Toaster as Sonner, type ToasterProps } from "sonner"
import {
  CircleCheckIcon,
  InfoIcon,
  TriangleAlertIcon,
  OctagonXIcon,
  Loader2Icon,
} from "lucide-react"

import * as React from "react"

import { usePref } from "@/lib/prefs"
import { useTheme } from "@/lib/theme"

// Where the shell's sidebar ends (app-shell.tsx): 200px, or 44px as a
// rail (chosen, or under 1000px).
const SIDEBAR = 200
const SIDEBAR_RAIL = 44
const NARROW = "(max-width: 1000px)"
// Above a page's bottom bar (the waterfall's transport row: 16px of page
// padding and 36px of bar).
const ABOVE_BOTTOM_BAR = 60

function useNarrow(): boolean {
  return React.useSyncExternalStore(
    (l) => {
      const m = window.matchMedia(NARROW)
      m.addEventListener("change", l)
      return () => m.removeEventListener("change", l)
    },
    () => window.matchMedia(NARROW).matches,
    () => false
  )
}

// Toasts (Toast README): card ground, border, radius-card, elev-card; a
// 15px icon; one sentence with its numbers. They sit at the bottom left
// of the content area: beside the sidebar, clear of a detail on the
// right and above a page's bottom bar.
const Toaster = ({ ...props }: ToasterProps) => {
  const { dark } = useTheme()
  const [sidebar] = usePref("wb.sidebar", "full")
  const narrow = useNarrow()
  const left = (sidebar === "rail" || narrow ? SIDEBAR_RAIL : SIDEBAR) + 16
  const icon = { strokeWidth: 1.75, className: "size-[15px]" }
  return (
    <Sonner
      theme={dark ? "dark" : "light"}
      className="toaster group"
      position="bottom-left"
      offset={{ left, bottom: ABOVE_BOTTOM_BAR }}
      mobileOffset={{ left: SIDEBAR_RAIL + 12, right: 12, bottom: 12 }}
      icons={{
        success: (
          <CircleCheckIcon
            {...icon}
            className="size-[15px] text-success-foreground"
          />
        ),
        info: <InfoIcon {...icon} />,
        warning: (
          <TriangleAlertIcon
            {...icon}
            className="size-[15px] text-warning-foreground"
          />
        ),
        error: (
          <OctagonXIcon
            {...icon}
            className="size-[15px] text-danger-foreground"
          />
        ),
        loading: <Loader2Icon {...icon} className="size-[15px] animate-spin" />,
      }}
      style={
        {
          "--normal-bg": "var(--card)",
          "--normal-text": "var(--card-foreground)",
          "--normal-border": "var(--border)",
          "--border-radius": "var(--radius-card)",
        } as React.CSSProperties
      }
      toastOptions={{
        classNames: {
          toast: "font-sans! shadow-card! text-sm!",
          description: "text-xs! text-muted-foreground!",
        },
      }}
      {...props}
    />
  )
}

export { Toaster }
