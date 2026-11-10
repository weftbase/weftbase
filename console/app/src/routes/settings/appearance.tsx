import { createFileRoute } from "@tanstack/react-router"

import { Section } from "@/components/console/page-header"
import { Kbd } from "@/components/ui/kbd"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { setTheme, useTheme } from "@/lib/theme"
import type { ThemePref } from "@/lib/theme"

// Settings, Appearance: the theme (kept in this browser) and the keys.
// Density is compact; there is no other setting.
export const Route = createFileRoute("/settings/appearance")({
  component: Appearance,
})

const OPTIONS: { value: ThemePref; label: string }[] = [
  { value: "system", label: "System" },
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
]

const KEYS: [string, string][] = [
  ["⌘K", "Open the command menu (Ctrl K on Linux and Windows)"],
  ["⌘B", "Collapse or expand the sidebar"],
  ["/", "Focus the search box of a list"],
  ["↑ ↓", "Move between rows; Enter opens the row"],
  ["Esc", "Clear the search, or close a menu or detail"],
]

function Appearance() {
  const { pref } = useTheme()
  return (
    <>
      <Section title="Theme">
        <RadioGroup
          value={pref}
          onValueChange={(v) => setTheme(v as ThemePref)}
          className="flex gap-4 rounded-alert border bg-card px-3 py-2"
        >
          {OPTIONS.map((o) => (
            <div key={o.value} className="flex items-center gap-2">
              <RadioGroupItem value={o.value} id={`theme-${o.value}`} />
              <Label htmlFor={`theme-${o.value}`} className="text-sm">
                {o.label}
              </Label>
            </div>
          ))}
        </RadioGroup>
      </Section>
      <Section title="Keyboard">
        <dl className="overflow-hidden rounded-alert border bg-card">
          {KEYS.map(([key, what]) => (
            <div
              key={key}
              className="flex h-8 items-center gap-4 border-b px-3 last:border-b-0"
            >
              <dt className="w-16 flex-none">
                <Kbd size="sm">{key}</Kbd>
              </dt>
              <dd className="text-sm text-muted-foreground">{what}</dd>
            </div>
          ))}
        </dl>
      </Section>
    </>
  )
}
