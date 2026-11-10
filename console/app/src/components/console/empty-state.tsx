import { useRouter } from "@tanstack/react-router"
import { InboxIcon, TriangleAlertIcon } from "lucide-react"
import type { LucideIcon } from "lucide-react"
import * as React from "react"

import { Button } from "@/components/ui/button"
import { ApiError, isNotFound, isUnsupported } from "@/lib/api/errors"
import { cn } from "@/lib/utils"

/** EmptyState: nothing to show, said plainly, with what to do next. */
export function EmptyState({
  icon: Icon = InboxIcon,
  title,
  children,
  action,
  className,
}: {
  icon?: LucideIcon
  title: string
  children?: React.ReactNode
  action?: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center gap-1 px-4 py-10 text-center",
        className
      )}
    >
      <Icon className="mb-1 size-5 text-subtle-foreground" aria-hidden />
      <p className="text-sm font-medium">{title}</p>
      {children && (
        <div className="max-w-sm text-xs text-muted-foreground">{children}</div>
      )}
      {action && <div className="mt-2">{action}</div>}
    </div>
  )
}

/** ErrorState: a request failed. It shows the server's own message, the
 * request id when there is one, and a retry. A 404 and a 501 read as
 * what they are, not as a crash. */
export function ErrorState({
  error,
  what = "This",
  onRetry,
  className,
}: {
  error: unknown
  /** What failed to load: "The run", "Sessions". */
  what?: string
  onRetry?: () => void
  className?: string
}) {
  const router = useRouter()
  const title = isNotFound(error)
    ? `${what} was not found`
    : isUnsupported(error)
      ? `${what} is not available on this server`
      : `${what} could not load`
  const message = error instanceof Error ? error.message : String(error)
  return (
    <EmptyState
      icon={TriangleAlertIcon}
      title={title}
      className={className}
      action={
        !isNotFound(error) &&
        !isUnsupported(error) && (
          <Button
            size="sm"
            variant="secondary"
            onClick={() => (onRetry ? onRetry() : void router.invalidate())}
          >
            Try again
          </Button>
        )
      }
    >
      <span className="block font-mono break-words">{message}</span>
      {error instanceof ApiError && error.requestId && (
        <span className="mt-1 block font-mono text-subtle-foreground">
          request {error.requestId}
        </span>
      )}
    </EmptyState>
  )
}

/** Skeleton rows for a loading list: `rows` lines at the row height. */
export function LoadingRows({ rows = 6 }: { rows?: number }) {
  return (
    <div role="status" aria-label="Loading" className="animate-pulse">
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="flex h-8 items-center border-b px-2">
          <div
            className="h-2.5 rounded-sm bg-muted"
            style={{ width: `${30 + ((i * 17) % 45)}%` }}
          />
        </div>
      ))}
    </div>
  )
}
