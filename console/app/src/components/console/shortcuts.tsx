import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Kbd } from "@/components/ui/kbd"

// The one sheet of keyboard shortcuts ("?" anywhere outside a field).

export const SHORTCUTS: { title: string; keys: [string[], string][] }[] = [
  {
    title: "Anywhere",
    keys: [
      [["⌘", "K"], "Command menu: pages, agents, a run or session by id"],
      [["⌘", "B"], "Collapse or expand the sidebar"],
      [["?"], "This sheet"],
      [["Esc"], "Close a menu, a dialog or the detail"],
    ],
  },
  {
    title: "Lists",
    keys: [
      [["/"], "Focus the search box"],
      [["↑", "↓"], "Move between rows (also j and k)"],
      [["Enter"], "Open the row"],
      [["Home", "End"], "First and last row"],
    ],
  },
  {
    title: "A run's timeline",
    keys: [
      [["↑", "↓"], "Move between steps"],
      [["←", "→"], "Fold or unfold a step"],
      [["Enter"], "Open the step's detail"],
      [["Space"], "Play or pause the playback"],
      [["⌘", "F"], "Search the steps"],
    ],
  },
]

export function ShortcutsSheet({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>⌘ is Ctrl on Linux and Windows.</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-3">
          {SHORTCUTS.map((g) => (
            <section key={g.title} className="flex flex-col gap-1">
              <h3 className="table-head">{g.title}</h3>
              <dl className="overflow-hidden rounded-alert border">
                {g.keys.map(([keys, what]) => (
                  <div
                    key={what}
                    className="flex min-h-7 items-center gap-3 border-b px-2 py-1 last:border-b-0"
                  >
                    <dt className="flex w-24 flex-none items-center gap-1">
                      {keys.map((k) => (
                        <Kbd key={k} size="sm">
                          {k}
                        </Kbd>
                      ))}
                    </dt>
                    <dd className="text-xs text-muted-foreground">{what}</dd>
                  </div>
                ))}
              </dl>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}
