import { Outlet, createFileRoute, useLocation } from "@tanstack/react-router"

import { LinkTabs, Page, PageHeader } from "@/components/console/page-header"

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
  const path = useLocation({ select: (l) => l.pathname })
  return (
    <Page
      header={<PageHeader title="Settings" />}
      bar={
        <LinkTabs
          tabs={SECTIONS.map((s) => ({
            label: s.label,
            link: { to: s.to },
            active: path.endsWith(s.to),
          }))}
        />
      }
    >
      <div className="flex max-w-3xl flex-col gap-4">
        <Outlet />
      </div>
    </Page>
  )
}
