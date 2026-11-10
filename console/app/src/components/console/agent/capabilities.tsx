import { useQuery } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ChevronRightIcon, ShieldCheckIcon } from "lucide-react"
import * as React from "react"

import { JsonView } from "@/components/console/json-view"
import { Fold, Muted, Text } from "@/components/console/run/fold"
import { q } from "@/lib/api/queries"
import type { ManifestTool, VersionDetail } from "@/lib/api/types"
import { limitRows, manifestAgent } from "@/lib/agents"
import { count } from "@/lib/format"
import { cn } from "@/lib/utils"

// Capabilities: what one version of an agent can do, read from its
// manifest. One container, four parts in a fixed order: Instructions
// (folded; the text is fetched when opened), Tools, Subagents, Limits.

/** The stored manifest's entry for this agent, fetched on demand. */
function useManifest(version: VersionDetail, enabled: boolean) {
  const m = useQuery({ ...q.manifest(version.manifest_hash), enabled })
  return {
    agent: m.data ? manifestAgent(m.data, version.agent) : undefined,
    error: m.error,
    loading: enabled && !m.data && !m.error,
  }
}

function Instructions({ version }: { version: VersionDetail }) {
  const m = useManifest(version, true)
  if (m.error)
    return <Muted>The instructions could not be read: {String(m.error)}</Muted>
  if (m.loading) return <Muted>Loading…</Muted>
  const text = m.agent?.instructions
  if (!text) return <Muted>The stored manifest has no instructions text.</Muted>
  return <Text>{text}</Text>
}

function ToolRow({
  tool,
  version,
}: {
  tool: ManifestTool
  version: VersionDetail
}) {
  const [open, setOpen] = React.useState(false)
  const m = useManifest(version, open)
  const schema = m.agent?.tools?.find((t) => t.name === tool.name)?.input_schema
  return (
    <li className="border-b border-table-border last:border-b-0">
      <div className="flex min-h-8 items-center gap-2 px-2 py-1">
        <button
          type="button"
          aria-expanded={open}
          aria-label={`${open ? "Hide" : "Show"} the input schema of ${tool.name}`}
          title="Input schema"
          onClick={() => setOpen((o) => !o)}
          className="flex size-5 flex-none items-center justify-center rounded-sm text-subtle-foreground outline-none hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
        >
          <ChevronRightIcon
            aria-hidden
            className={cn("size-3.5 transition-transform", open && "rotate-90")}
          />
        </button>
        <span className="w-44 flex-none truncate font-mono text-xs font-medium">
          {tool.name}
        </span>
        <span className="min-w-0 flex-1 truncate text-sm text-muted-foreground">
          {tool.description || "—"}
        </span>
        {tool.timeout && (
          <span className="flex-none font-mono text-xs text-subtle-foreground">
            timeout {tool.timeout}
          </span>
        )}
        {tool.subagent && (
          <Link
            to="/agents/$name"
            params={{ name: tool.subagent }}
            className="flex-none text-xs text-muted-foreground hover:text-foreground hover:underline"
          >
            hands off to {tool.subagent}
          </Link>
        )}
        {tool.needs_approval && (
          <span className="inline-flex h-5 flex-none items-center gap-1 rounded-sm bg-warning-muted px-1.5 text-xs font-medium text-warning-foreground">
            <ShieldCheckIcon aria-hidden className="size-3" />
            needs approval
          </span>
        )}
      </div>
      {open && (
        <div className="px-9 pb-2">
          {m.error ? (
            <Muted>The schema could not be read: {String(m.error)}</Muted>
          ) : m.loading ? (
            <Muted>Loading…</Muted>
          ) : schema === undefined ? (
            <Muted>
              The stored manifest has no input schema for this tool.
            </Muted>
          ) : (
            <JsonView value={schema} depth={3} />
          )}
        </div>
      )}
    </li>
  )
}

export function Capabilities({ version }: { version: VersionDetail }) {
  const m = version.manifest
  if (!m)
    return (
      <p className="rounded-alert border bg-card px-3 py-2 text-xs text-muted-foreground">
        The manifest this version registered could not be read, so its
        instructions, tools and limits are not known.
      </p>
    )
  const tools = m.tools ?? []
  const subagents = m.subagents ?? []
  const gated = tools.filter((t) => t.needs_approval).length
  const limits = limitRows(m.limits)
  return (
    <div className="overflow-hidden rounded-alert border bg-card">
      <Fold
        id="agent.instructions"
        title="Instructions"
        open={false}
        hint={
          m.instructions.present
            ? `${count(m.instructions.length)} bytes`
            : "none"
        }
      >
        {m.instructions.present ? (
          <Instructions version={version} />
        ) : (
          <Muted>This version has no instructions.</Muted>
        )}
      </Fold>
      <Fold
        id="agent.tools"
        title="Tools"
        hint={
          tools.length === 0
            ? "none"
            : `${count(tools.length)}${gated > 0 ? ` · ${count(gated)} ${gated === 1 ? "needs" : "need"} approval` : ""}`
        }
      >
        {tools.length === 0 ? (
          <Muted>This version has no tools.</Muted>
        ) : (
          <ul className="-mx-3 -mb-3 border-t border-table-border">
            {tools.map((t) => (
              <ToolRow key={t.name} tool={t} version={version} />
            ))}
          </ul>
        )}
      </Fold>
      {subagents.length > 0 && (
        <Fold
          id="agent.subagents"
          title="Subagents"
          hint={count(subagents.length)}
        >
          <ul className="flex flex-wrap gap-x-4 gap-y-1 text-sm">
            {subagents.map((s) => (
              <li key={s}>
                <Link
                  to="/agents/$name"
                  params={{ name: s }}
                  className="font-medium underline-offset-4 hover:underline"
                >
                  {s}
                </Link>
              </li>
            ))}
          </ul>
        </Fold>
      )}
      <Fold id="agent.limits" title="Limits" hint={count(limits.length)}>
        {limits.length === 0 ? (
          <Muted>This version sets no limits.</Muted>
        ) : (
          <dl className="grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-x-6 gap-y-1 text-xs">
            {limits.map(([k, v]) => (
              <div key={k} className="flex min-w-0 items-baseline gap-2">
                <dt className="flex-none text-muted-foreground">{k}</dt>
                <dd className="min-w-0 truncate font-mono" title={v}>
                  {v}
                </dd>
              </div>
            ))}
          </dl>
        )}
      </Fold>
    </div>
  )
}
