import { Toaster as Sonner, type ToasterProps } from "sonner"
import {
  CircleCheckIcon,
  InfoIcon,
  TriangleAlertIcon,
  OctagonXIcon,
  Loader2Icon,
} from "lucide-react"

import { useTheme } from "@/lib/theme"

// Toasts (Toast README): card ground, border, radius-card, elev-card; a
// 15px icon; one sentence with its numbers.
const Toaster = ({ ...props }: ToasterProps) => {
  const { dark } = useTheme()
  const icon = { strokeWidth: 1.75, className: "size-[15px]" }
  return (
    <Sonner
      theme={dark ? "dark" : "light"}
      className="toaster group"
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
