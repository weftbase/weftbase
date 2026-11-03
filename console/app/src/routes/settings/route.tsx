import { Link, Outlet, createFileRoute } from "@tanstack/react-router"

import { PageHeader } from "@/components/console/page-header"

const SECTIONS = [
  { to: "/settings/general", label: "General" },
  { to: "/settings/pricing", label: "Pricing" },
  { to: "/settings/appearance", label: "Appearance" },
] as const

export const Route = createFileRoute("/settings")({
  head: () => ({ meta: [{ title: "Settings · weftbase" }] }),
  component: Settings,
})

function Settings() {
  return (
    <div className="mx-auto flex max-w-[1000px] flex-col gap-6">
      <PageHeader
        title="Settings"
        description="How this server is set up, and how the Console looks to you."
      />
      <div className="flex flex-wrap gap-8">
        <nav
          aria-label="Settings"
          className="flex w-[180px] flex-none flex-col gap-1 max-[760px]:w-full max-[760px]:flex-row max-[760px]:flex-wrap"
        >
          {SECTIONS.map((s) => (
            <Link
              key={s.to}
              to={s.to}
              className="flex h-9 items-center rounded-md px-2.5 text-sm font-medium text-muted-foreground hover:bg-accent hover:text-foreground aria-[current=page]:bg-accent aria-[current=page]:text-foreground aria-[current=page]:shadow-nav-active"
            >
              {s.label}
            </Link>
          ))}
        </nav>
        <div className="min-w-0 flex-1">
          <Outlet />
        </div>
      </div>
    </div>
  )
}
