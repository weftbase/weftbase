import * as React from "react"

// PageHeader (design system): the title, one sentence that says what the
// page holds, and its filters on the right, aligned to the bottom edge.
export function PageHeader({
  title,
  description,
  eyebrow,
  children,
}: {
  title: React.ReactNode
  description?: React.ReactNode
  eyebrow?: React.ReactNode
  children?: React.ReactNode
}) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-4">
      <div className="min-w-0">
        {eyebrow && <div className="mb-1.5 eyebrow">{eyebrow}</div>}
        <h1 className="text-2xl leading-tight font-semibold tracking-[-0.02em]">
          {title}
        </h1>
        {description && (
          <p className="mt-1 text-sm text-muted-foreground">{description}</p>
        )}
      </div>
      {children && (
        <div className="flex flex-wrap items-center gap-3">{children}</div>
      )}
    </div>
  )
}
