import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { PlayIcon } from "lucide-react"
import * as React from "react"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api/http"
import { q } from "@/lib/api/queries"

// "New run" (CONSOLE-UX, New run): pick an agent and a version or label,
// type the input, Start. It calls POST /v1/runs and opens the run's
// page, which shows it queued and then live. No saved prompts, no model
// overrides. The top bar renders <NewRun />; an agent's page renders
// <NewRun agent="support" /> to preset the agent.

const DEFAULT = "default"

export function NewRun({
  agent: preset,
  variant = "secondary",
}: {
  /** Preselect this agent (an agent's page). */
  agent?: string
  variant?: "default" | "secondary"
}) {
  const [open, setOpen] = React.useState(false)
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant={variant}>
          <PlayIcon />
          New run
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-lg">
        {open && <Form preset={preset} onDone={() => setOpen(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function Form({ preset, onDone }: { preset?: string; onDone: () => void }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const agents = useQuery(q.agents())
  const [agent, setAgent] = React.useState(preset ?? "")
  const [version, setVersion] = React.useState(DEFAULT)
  const [prompt, setPrompt] = React.useState("")
  const list = agents.data ?? []
  const name = agent || (list.length === 1 ? list[0].name : "")
  const current = list.find((a) => a.name === name)
  const versions = useQuery({
    ...q.agentVersions(name),
    enabled: name !== "",
  })
  const labels = Object.keys(current?.labels ?? {}).sort()
  const start = useMutation({
    mutationFn: () =>
      api.admit({
        agent: name,
        version: version === DEFAULT ? undefined : version,
        input: { prompt: prompt.trim() },
      }),
    onSuccess: (run) => {
      void qc.invalidateQueries({ queryKey: ["runs"] })
      onDone()
      void navigate({ to: "/runs/$runId", params: { runId: run.id } })
    },
  })
  const ready = name !== "" && prompt.trim() !== "" && !start.isPending
  const submit = () => {
    if (ready) start.mutate()
  }

  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <DialogHeader>
        <DialogTitle>New run</DialogTitle>
        <DialogDescription>
          Starts a real run of the agent on this server and opens it.
        </DialogDescription>
      </DialogHeader>
      <div className="grid grid-cols-2 gap-3">
        <label className="flex min-w-0 flex-col gap-1 text-xs text-muted-foreground">
          Agent
          <Select
            value={name}
            onValueChange={(v) => {
              setAgent(v)
              setVersion(DEFAULT)
            }}
          >
            <SelectTrigger className="w-full" aria-label="Agent">
              <SelectValue
                placeholder={agents.isLoading ? "Loading…" : "Choose an agent"}
              />
            </SelectTrigger>
            <SelectContent>
              {list.map((a) => (
                <SelectItem key={a.name} value={a.name}>
                  {a.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </label>
        <label className="flex min-w-0 flex-col gap-1 text-xs text-muted-foreground">
          Version
          <Select value={version} onValueChange={setVersion} disabled={!name}>
            <SelectTrigger className="w-full" aria-label="Version or label">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={DEFAULT}>
                Default{labels.includes("prod") ? " (prod)" : " (newest)"}
              </SelectItem>
              {labels.length > 0 && (
                <SelectGroup>
                  <SelectLabel>Labels</SelectLabel>
                  {labels.map((l) => (
                    <SelectItem key={l} value={`label:${l}`}>
                      {l}
                    </SelectItem>
                  ))}
                </SelectGroup>
              )}
              {(versions.data ?? []).length > 0 && (
                <SelectGroup>
                  <SelectLabel>Versions</SelectLabel>
                  {(versions.data ?? []).map((v) => (
                    <SelectItem key={v.id} value={v.id}>
                      v{v.number}
                      {v.labels && v.labels.length > 0 && (
                        <span className="text-subtle-foreground">
                          {v.labels.join(", ")}
                        </span>
                      )}
                    </SelectItem>
                  ))}
                </SelectGroup>
              )}
            </SelectContent>
          </Select>
        </label>
      </div>
      <label className="flex flex-col gap-1 text-xs text-muted-foreground">
        Input
        <Textarea
          autoFocus
          rows={5}
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault()
              submit()
            }
          }}
          placeholder="What the user says to the agent"
          className="text-foreground"
        />
      </label>
      {agents.error && (
        <p role="alert" className="text-xs text-danger-foreground">
          The agents could not be listed: {String(agents.error)}
        </p>
      )}
      {agents.data && list.length === 0 && (
        <p className="text-xs text-muted-foreground">
          No agent is registered on this server yet.
        </p>
      )}
      {start.error && (
        <p role="alert" className="text-xs text-danger-foreground">
          {start.error instanceof Error
            ? start.error.message
            : String(start.error)}
        </p>
      )}
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onDone}>
          Close
        </Button>
        <Button type="submit" disabled={!ready}>
          {start.isPending ? "Starting…" : "Start"}
        </Button>
      </DialogFooter>
    </form>
  )
}
