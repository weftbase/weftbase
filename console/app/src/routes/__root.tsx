import { QueryClientProvider } from "@tanstack/react-query"
import {
  HeadContent,
  Link,
  Scripts,
  createRootRouteWithContext,
  retainSearchParams,
  useRouter,
} from "@tanstack/react-router"
import type { ErrorComponentProps } from "@tanstack/react-router"
import {
  FileQuestionIcon,
  LucideProvider,
  TriangleAlertIcon,
} from "lucide-react"

import { AppShell } from "@/components/console/app-shell"
import { EmptyState } from "@/components/console/empty-state"
import { Button } from "@/components/ui/button"
import { Toaster } from "@/components/ui/sonner"
import { TooltipProvider } from "@/components/ui/tooltip"
import { validateRoot } from "@/lib/search"
import { themeBootstrap } from "@/lib/theme"
import type { RouterContext } from "@/router"
import appCss from "../styles.css?url"

export const Route = createRootRouteWithContext<RouterContext>()({
  validateSearch: validateRoot,
  // The environment follows you from page to page.
  search: { middlewares: [retainSearchParams(["env"])] },
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
          <AppShell />
          <Toaster position="bottom-right" />
        </TooltipProvider>
      </LucideProvider>
    </QueryClientProvider>
  )
}

export function NotFound() {
  return (
    <div className="mx-auto max-w-md pt-16">
      <EmptyState icon={FileQuestionIcon} title="Nothing at this address">
        The link may be old, or the item was deleted.{" "}
        <Link
          to="/"
          className="font-medium text-foreground underline underline-offset-4"
        >
          Go to the overview
        </Link>
      </EmptyState>
    </div>
  )
}

function RouteError({ error }: ErrorComponentProps) {
  const router = useRouter()
  return (
    <div className="mx-auto max-w-md pt-16">
      <EmptyState icon={TriangleAlertIcon} title="This page could not load">
        <span className="block font-mono text-xs">
          {error instanceof Error ? error.message : String(error)}
        </span>
        <Button
          size="sm"
          variant="secondary"
          className="mt-4"
          onClick={() => void router.invalidate()}
        >
          Try again
        </Button>
      </EmptyState>
    </div>
  )
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
