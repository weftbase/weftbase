import { createFileRoute } from "@tanstack/react-router"

import { Section } from "@/components/console/page-header"
import { Kbd } from "@/components/ui/kbd"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { setTheme, useTheme } from "@/lib/theme"
import type { ThemePref } from "@/lib/theme"

// Settings, Appearance: the theme, kept in this browser.
export const Route = createFileRoute("/settings/appearance")({
  component: Appearance,
})

const OPTIONS: { value: ThemePref; label: string }[] = [
  { value: "system", label: "System" },
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
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
      <p className="text-xs text-muted-foreground">
        Kept in this browser. Density is compact; there is no other setting.
        Press <Kbd size="sm">?</Kbd> anywhere for the keyboard shortcuts.
      </p>
    </>
  )
}
