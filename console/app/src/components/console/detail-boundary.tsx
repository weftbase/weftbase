import { TriangleAlertIcon, XIcon } from "lucide-react"
import * as React from "react"

import { EmptyState } from "@/components/console/empty-state"
import { Button } from "@/components/ui/button"

/** DetailBoundary keeps a detail that fails to render inside its own
 * container: the page around it stays. Key it by what it shows, so the
 * next selection starts clean. */
export class DetailBoundary extends React.Component<
  { children: React.ReactNode; onClose?: () => void },
  { error: unknown }
> {
  state: { error: unknown } = { error: null }

  static getDerivedStateFromError(error: unknown) {
    return { error }
  }

  componentDidCatch(error: unknown) {
    console.error("The detail could not be shown:", error)
  }

  render() {
    if (this.state.error === null) return this.props.children
    const message =
      this.state.error instanceof Error
        ? this.state.error.message
        : String(this.state.error)
    return (
      <div
        role="alert"
        className="flex h-full min-h-0 flex-col overflow-hidden rounded-alert border bg-card"
      >
        {this.props.onClose && (
          <div className="flex h-9 flex-none items-center justify-end border-b pr-1.5">
            <Button
              size="icon-sm"
              variant="ghost"
              aria-label="Close detail"
              title="Close (Esc)"
              onClick={this.props.onClose}
            >
              <XIcon />
            </Button>
          </div>
        )}
        <EmptyState
          icon={TriangleAlertIcon}
          title="This row's detail could not be shown"
          action={
            <Button
              size="sm"
              variant="secondary"
              onClick={() => this.setState({ error: null })}
            >
              Try again
            </Button>
          }
        >
          {message}
        </EmptyState>
      </div>
    )
  }
}
