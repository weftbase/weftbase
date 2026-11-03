import { createFileRoute } from "@tanstack/react-router"

import { Card } from "@/components/ui/card"
import { Kbd } from "@/components/ui/kbd"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { setTheme, useTheme } from "@/lib/theme"
import type { ThemePref } from "@/lib/theme"

export const Route = createFileRoute("/settings/appearance")({
  component: Appearance,
})

const OPTIONS: { value: ThemePref; label: string; hint: string }[] = [
  { value: "system", label: "System", hint: "Follow your operating system." },
  { value: "light", label: "Light", hint: "Graphite on white." },
  {
    value: "dark",
    label: "Dark",
    hint: "A full dark theme. Code blocks stay dark in both.",
  },
]

const KEYS: [string, string][] = [
  ["⌘K", "Open the command menu"],
  ["Ctrl K", "Open the command menu on Linux and Windows"],
  ["Esc", "Close a menu or dialog"],
]

function Appearance() {
  const { pref } = useTheme()
  return (
    <section className="flex flex-col gap-6">
      <div>
        <h2 className="text-lg">Appearance</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Saved in this browser only.
        </p>
      </div>
      <Card className="gap-4 p-5">
        <h3 className="text-sm">Theme</h3>
        <RadioGroup
          value={pref}
          onValueChange={(v) => setTheme(v as ThemePref)}
          aria-label="Theme"
        >
          {OPTIONS.map((o) => (
            <div key={o.value} className="flex items-start gap-2.5">
              <RadioGroupItem
                value={o.value}
                id={`theme-${o.value}`}
                className="mt-px"
              />
              <Label
                htmlFor={`theme-${o.value}`}
                className="flex-col items-start gap-1 text-sm"
              >
                {o.label}
                <span className="text-xs font-normal text-subtle-foreground">
                  {o.hint}
                </span>
              </Label>
            </div>
          ))}
        </RadioGroup>
      </Card>
      <Card className="gap-4 p-5">
        <h3 className="text-sm">Keyboard</h3>
        <dl className="flex flex-col gap-3">
          {KEYS.map(([k, v]) => (
            <div
              key={k}
              className="flex items-center justify-between gap-4 text-sm"
            >
              <dt className="text-muted-foreground">{v}</dt>
              <dd>
                <Kbd>{k}</Kbd>
              </dd>
            </div>
          ))}
        </dl>
      </Card>
    </section>
  )
}
