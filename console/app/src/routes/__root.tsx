import { QueryClientProvider } from "@tanstack/react-query"
import {
  HeadContent,
  Link,
  Scripts,
  createRootRouteWithContext,
} from "@tanstack/react-router"
import type { ErrorComponentProps } from "@tanstack/react-router"
import { FileQuestionIcon, LucideProvider } from "lucide-react"

import { AuthGate } from "@/components/console/auth-gate"
import { EmptyState, ErrorState } from "@/components/console/empty-state"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { themeBootstrap } from "@/lib/theme"
import type { RouterContext } from "@/router"
import appCss from "../styles.css?url"

export const Route = createRootRouteWithContext<RouterContext>()({
  head: () => ({
    meta: [
      { charSet: "utf-8" },
      { name: "viewport", content: "width=device-width, initial-scale=1" },
      { name: "color-scheme", content: "light dark" },
      { title: "weftbase" },
    ],
    links: [{ rel: "stylesheet", href: appCss }],
  }),
  component: RootComponent,
  notFoundComponent: NotFound,
  errorComponent: RouteError,
  shellComponent: RootDocument,
})

function RootComponent() {
  const { queryClient } = Route.useRouteContext()
  return (
    <QueryClientProvider client={queryClient}>
      <LucideProvider strokeWidth={1.75}>
        <TooltipProvider delayDuration={200}>
          <AuthGate />
          <Toaster />
        </TooltipProvider>
      </LucideProvider>
    </QueryClientProvider>
  )
}

export function NotFound() {
  return (
    <EmptyState icon={FileQuestionIcon} title="Nothing at this address">
      The link may be old, or the item was deleted.{" "}
      <Link
        to="/"
        className="font-medium text-foreground underline underline-offset-4"
      >
        Go to the overview
      </Link>
    </EmptyState>
  )
}

function RouteError({ error }: ErrorComponentProps) {
  return <ErrorState error={error} what="This page" />
}

// The <base> is React-owned and carries the runtime mount, so hydration
// adopts the element the Go handler wrote instead of replacing it. Every
// relative URL (assets, fonts, API calls) resolves under the mount.
function mountBase(): string {
  if (typeof document === "undefined") return "/"
  return new URL(document.baseURI).pathname
}

function RootDocument({ children }: { children: React.ReactNode }) {
  return (
    // The bootstrap sets the theme class before first paint.
    <html lang="en" suppressHydrationWarning>
      <head>
        <base href={mountBase()} />
        <script dangerouslySetInnerHTML={{ __html: themeBootstrap }} />
        <link rel="icon" type="image/svg+xml" href="favicon.svg" />
        <HeadContent />
      </head>
      <body>
        {children}
        <Scripts />
      </body>
    </html>
  )
}
