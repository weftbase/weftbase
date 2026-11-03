// Typed mock data for the Console while the /v1 server is being built.
// Deterministic (a seeded generator), with times relative to when the
// module loads so "2 min ago" reads right. Replaced by the /v1 goldens
// (api/http/v1/testdata) once they exist.

import type {
  Agent,
  Approval,
  Price,
  Run,
  RunStatus,
  Session,
  SessionEntry,
  SessionTurn,
  Span,
  Step,
  Trace,
} from "./types"
import { argsSha256 } from "./args-hash"

const NOW = Date.now()
const MIN = 60_000
const iso = (msAgo: number) => new Date(NOW - msAgo).toISOString()

function rng(seed: number) {
  let s = seed >>> 0
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 2 ** 32
  }
}
const rand = rng(20261103)
const pick = <T>(xs: readonly T[]): T => xs[Math.floor(rand() * xs.length)]
const int = (lo: number, hi: number) => lo + Math.floor(rand() * (hi - lo + 1))
const hex = (n: number) =>
  Array.from({ length: n }, () => "0123456789abcdef"[int(0, 15)]).join("")

const PRICES: Price[] = [
  {
    provider: "anthropic",
    model: "claude-sonnet-4-5",
    input_per_mtok: 3,
    output_per_mtok: 15,
    source: "built-in",
  },
  {
    provider: "anthropic",
    model: "claude-haiku-4-5",
    input_per_mtok: 1,
    output_per_mtok: 5,
    source: "built-in",
  },
  {
    provider: "openai",
    model: "gpt-5-mini",
    input_per_mtok: 0.25,
    output_per_mtok: 2,
    source: "built-in",
  },
  {
    provider: "local",
    model: "llama-3.3-70b",
    input_per_mtok: 0.6,
    output_per_mtok: 0.6,
    source: "override",
  },
]

function priceOf(model: string, input: number, output: number): number | null {
  const p = PRICES.find((x) => `${x.provider}/${x.model}` === model)
  if (!p) return null
  return (input * p.input_per_mtok + output * p.output_per_mtok) / 1_000_000
}

interface AgentSeed {
  name: string
  description: string
  model: string
  tools: string[]
  approval_tools: string[]
  middleware: string[]
}

const AGENT_SEEDS: AgentSeed[] = [
  {
    name: "support-triage",
    description:
      "Reads a support ticket, finds the customer and routes it to the right queue.",
    model: "anthropic/claude-haiku-4-5",
    tools: ["lookup_customer", "search_docs", "route_ticket"],
    approval_tools: [],
    middleware: ["retry", "budget", "redact_pii"],
  },
  {
    name: "billing-refund",
    description:
      "Checks a refund request against policy and issues the refund after a person approves it.",
    model: "anthropic/claude-sonnet-4-5",
    tools: ["lookup_invoice", "issue_refund", "email_customer"],
    approval_tools: ["issue_refund"],
    middleware: ["retry", "budget", "approval"],
  },
  {
    name: "research-pool",
    description:
      "Splits a research question across subagents and writes one cited answer.",
    model: "anthropic/claude-sonnet-4-5",
    tools: ["web_search", "fetch_page", "policy-checker"],
    approval_tools: [],
    middleware: ["retry", "budget"],
  },
  {
    name: "docs-answerer",
    description: "Answers product questions from the docs index, with links.",
    model: "openai/gpt-5-mini",
    tools: ["search_docs"],
    approval_tools: [],
    middleware: ["retry"],
  },
  {
    name: "invoice-extract",
    description:
      "Pulls line items out of uploaded invoices into structured output.",
    model: "local/llama-3.3-70b",
    tools: ["read_file", "write_record"],
    approval_tools: ["write_record"],
    middleware: ["retry", "schema"],
  },
]

const TENANTS = ["acme", "globex", "initech"] as const
const ERROR_CLASSES = [
  "provider_rate_limited",
  "tool_error",
  "schema_invalid",
  "budget_exceeded",
] as const

function makeSteps(
  seed: AgentSeed,
  status: RunStatus,
  runId: string
): { steps: Step[]; input: number; output: number } {
  const steps: Step[] = []
  let t = 0
  let input = 0
  let output = 0
  const n = int(2, 5)
  for (let i = 0; i < n; i++) {
    const isModel = i % 2 === 0
    const last = i === n - 1
    if (isModel) {
      const inTok = int(600, 4200)
      const outTok = int(80, 900)
      input += inTok
      output += outTok
      const d = int(400, 2600)
      steps.push({
        index: steps.length,
        kind: "model",
        name: seed.model,
        status:
          last && status === "failed"
            ? "error"
            : last && status === "running"
              ? "running"
              : "ok",
        offset_ms: t,
        duration_ms: last && status === "running" ? null : d,
        summary: `${seed.model.split("/")[1]} · ${(inTok + outTok).toLocaleString("en-US")} tokens`,
        usage: { input_tokens: inTok, output_tokens: outTok },
        cost: priceOf(seed.model, inTok, outTok),
        request: {
          model: seed.model,
          max_tokens: 1024,
          messages: [{ role: "user", content: "[Ticket text]" }],
          tools: seed.tools,
        },
        response:
          last && status === "failed"
            ? {
                error: {
                  type: "rate_limit_error",
                  message: "Too many requests",
                },
              }
            : {
                stop_reason: "tool_use",
                content: [
                  {
                    type: "tool_use",
                    name: seed.tools[0],
                    input: { id: "INV-2291" },
                  },
                ],
              },
      })
      t += d
    } else {
      // A tool that needs approval only runs after a person approves it.
      const free = seed.tools.filter((x) => !seed.approval_tools.includes(x))
      const tool = free[(i >> 1) % free.length]
      const isSub = tool === "policy-checker"
      const d = int(80, 1400)
      steps.push({
        index: steps.length,
        kind: isSub ? "subagent" : "tool",
        name: tool,
        status: "ok",
        offset_ms: t,
        duration_ms: d,
        summary: isSub ? "refund within policy" : `${tool} returned 1 result`,
        usage: null,
        cost: null,
        request: { id: "INV-2291" },
        response: {
          ok: true,
          result: { id: "INV-2291", amount: 129, currency: "USD" },
        },
        ...(isSub ? { child_run_id: `${runId}/${i}/0` } : {}),
      })
      t += d
    }
  }
  if (status === "waiting" && seed.approval_tools.length > 0) {
    steps.push({
      index: steps.length,
      kind: "approval",
      name: seed.approval_tools[0],
      status: "waiting",
      offset_ms: t,
      duration_ms: null,
      summary: "$129.00 to customer #4471",
      usage: null,
      cost: null,
      request: { customer_id: 4471, amount: 129, currency: "USD" },
      response: null,
      approval_id: `apr_${runId.slice(4)}`,
    })
  }
  return { steps, input, output }
}

const STATUS_POOL: RunStatus[] = [
  "succeeded",
  "succeeded",
  "succeeded",
  "succeeded",
  "succeeded",
  "succeeded",
  "running",
  "failed",
  "failed",
  "queued",
  "cancelled",
]

// Two sessions per agent, so a session holds several turns.
const SESSION_IDS = new Map<string, string>()
function sessionId(agent: string, n: number): string {
  const key = `${agent}#${n}`
  if (!SESSION_IDS.has(key)) SESSION_IDS.set(key, `ses_${hex(6)}`)
  return SESSION_IDS.get(key)!
}

function makeRuns(): Run[] {
  const runs: Run[] = []
  let ago = 0
  for (let i = 0; i < 48; i++) {
    const seed = i < 6 ? AGENT_SEEDS[[0, 2, 1, 3, 4, 0][i]] : pick(AGENT_SEEDS)
    let status: RunStatus =
      i < 6
        ? (
            [
              "succeeded",
              "running",
              "waiting",
              "succeeded",
              "failed",
              "succeeded",
            ] as const
          )[i]
        : pick(STATUS_POOL)
    if (status === "waiting" && seed.approval_tools.length === 0)
      status = "succeeded"
    if (i === 17 || i === 29)
      status = seed.approval_tools.length ? "waiting" : status
    const id = `run_${(0x8f2a1c - i * 0x6b).toString(16)}`
    const { steps, input, output } = makeSteps(seed, status, id)
    const done = !["running", "waiting", "queued"].includes(status)
    const duration = steps.reduce((a, s) => a + (s.duration_ms ?? 0), 0)
    const own = priceOf(seed.model, input, output)
    const hasSub = steps.some((s) => s.kind === "subagent")
    const errorClass = status === "failed" ? pick(ERROR_CLASSES) : null
    runs.push({
      id,
      agent: seed.name,
      agent_version: `v${int(3, 9)}`,
      status,
      model: seed.model,
      tenant: pick(TENANTS),
      session_id: i % 3 === 0 ? sessionId(seed.name, i % 2) : null,
      started_at: iso(ago),
      duration_ms: done ? duration : null,
      usage: { input_tokens: input, output_tokens: output },
      cost: own,
      delegated_cost: hasSub ? (own ?? 0) * 0.4 : null,
      error_class: errorClass,
      tools: [
        ...new Set(steps.filter((s) => s.kind !== "model").map((s) => s.name)),
      ],
      tags: i % 4 === 0 ? ["nightly"] : [],
      trace_id: hex(32),
      input: "Customer asks for a refund on invoice INV-2291.",
      output:
        status === "succeeded"
          ? "Ticket routed to billing with a refund recommendation."
          : null,
      error: errorClass
        ? `${errorClass}: the provider returned 429 after 3 attempts`
        : null,
      steps,
      transcript: [
        { role: "system", text: `You are ${seed.name}. ${seed.description}` },
        {
          role: "user",
          text: "Customer asks for a refund on invoice INV-2291.",
        },
        {
          role: "assistant",
          text: `I'll look up the invoice first with ${seed.tools[0]}.`,
        },
        {
          role: "tool",
          text: '{"id":"INV-2291","amount":129,"currency":"USD"}',
        },
        ...(status === "succeeded"
          ? [
              {
                role: "assistant" as const,
                text: "The refund is within policy. Routing to billing.",
              },
            ]
          : []),
      ],
    })
    ago += int(1, 9) * MIN + int(0, 59) * 1000
  }
  return runs
}

export const RUNS: Run[] = makeRuns()

export const APPROVALS: Approval[] = (() => {
  const pending: Approval[] = RUNS.filter((r) => r.status === "waiting").map(
    (r) => {
      const step = r.steps.find((s) => s.kind === "approval")
      const tool = step?.name ?? "issue_refund"
      const args =
        tool === "issue_refund"
          ? {
              customer_id: 4471,
              invoice: "INV-2291",
              amount: 129.0,
              currency: "USD",
              reason: "duplicate charge",
            }
          : {
              table: "invoices",
              record: { vendor: "[Vendor name]", total: 1840.5, lines: 12 },
            }
      return {
        id: step?.approval_id ?? `apr_${r.id.slice(4)}`,
        run_id: r.id,
        agent: r.agent,
        tool,
        tenant: r.tenant,
        status: "pending",
        requested_at: r.started_at,
        summary:
          tool === "issue_refund"
            ? "Refund $129.00 to customer #4471 for a duplicate charge on INV-2291."
            : "Write 12 extracted invoice lines to the invoices table.",
        args,
        args_sha256: argsSha256(args),
        decided_at: null,
        decided_by: null,
        reason: null,
      }
    }
  )
  const decided: Approval[] = RUNS.filter(
    (r) => r.agent === "billing-refund" && r.status === "succeeded"
  )
    .slice(0, 4)
    .map((r, i) => ({
      id: `apr_${r.id.slice(4)}`,
      run_id: r.id,
      agent: r.agent,
      tool: "issue_refund",
      tenant: r.tenant,
      status: i === 2 ? "denied" : "approved",
      requested_at: r.started_at,
      summary: `Refund $${(40 + i * 17).toFixed(2)} to customer #${4400 + i}.`,
      args: { customer_id: 4400 + i, amount: 40 + i * 17, currency: "USD" },
      args_sha256: argsSha256({
        customer_id: 4400 + i,
        amount: 40 + i * 17,
        currency: "USD",
      }),
      decided_at: r.started_at,
      decided_by: "[Operator name]",
      reason: i === 2 ? "Outside the 30-day window." : null,
    }))
  return [...pending, ...decided]
})()

export const AGENTS: Agent[] = AGENT_SEEDS.map((s, i) => {
  const runs = RUNS.filter((r) => r.agent === s.name)
  const durations = runs
    .map((r) => r.duration_ms)
    .filter((d): d is number => d !== null)
    .sort((a, b) => a - b)
  const costs = runs.map((r) => r.cost)
  const versions = Array.from({ length: 3 + (i % 2) }, (_, k) => {
    const v = 9 - i - k
    return {
      version: `v${v}`,
      manifest_hash: `sha256:${hex(12)}`,
      created_at: iso((k * 3 + 1) * 24 * 60 * MIN),
      labels: [] as ("dev" | "staging" | "prod")[],
      model: s.model,
      note:
        [
          "Tighter tool descriptions",
          "Adds the budget middleware",
          "Retries on rate limits",
          "First version",
        ][k] ?? "First version",
    }
  })
  versions[0].labels.push("dev")
  versions[i % 2 === 0 ? 0 : 1].labels.push("staging")
  versions[1].labels.push("prod")
  const labels: Agent["labels"] = {}
  for (const v of versions) for (const l of v.labels) labels[l] = v.version
  return {
    ...s,
    budget:
      s.name === "research-pool"
        ? { max_tokens: 200_000, max_cost: 2 }
        : { max_tokens: 50_000, max_cost: 0.5 },
    labels,
    versions,
    stats: {
      runs_24h: runs.length,
      failed_24h: runs.filter((r) => r.status === "failed").length,
      p50_ms: durations.length
        ? durations[Math.floor(durations.length / 2)]
        : 0,
      cost_24h: costs.some((c) => c === null)
        ? null
        : costs.reduce<number>((a, c) => a + (c ?? 0), 0),
    },
  }
})

export const SESSIONS: Session[] = (() => {
  const ids = [
    ...new Set(
      RUNS.map((r) => r.session_id).filter((x): x is string => x !== null)
    ),
  ]
  return ids.map((id, i) => {
    const runs = RUNS.filter((r) => r.session_id === id)
    const first = runs[runs.length - 1]
    const turns: SessionTurn[] = runs
      .slice()
      .reverse()
      .map((r, k) => ({
        index: k,
        run_id: r.id,
        status: r.status,
        at: r.started_at,
        input: k === 0 ? r.input : "Can you also check the second invoice?",
        output: r.output,
        agent_version: r.agent_version,
      }))
    const timeline: SessionEntry[] = turns.flatMap((t) => {
      const e: SessionEntry[] = [
        { at: t.at, kind: "turn", text: t.input, run_id: t.run_id },
      ]
      if (t.status === "waiting")
        e.push({
          at: t.at,
          kind: "approval",
          text: "Waiting for a person to approve issue_refund",
          run_id: t.run_id,
        })
      return e
    })
    if (i === 1)
      timeline.push({
        at: first.started_at,
        kind: "compaction",
        text: "History compacted to 2,048 tokens",
      })
    const usage = runs.reduce(
      (a, r) => ({
        input_tokens: a.input_tokens + r.usage.input_tokens,
        output_tokens: a.output_tokens + r.usage.output_tokens,
      }),
      { input_tokens: 0, output_tokens: 0 }
    )
    const waiting = runs.some((r) => r.status === "waiting")
    const running = runs.some((r) => r.status === "running")
    return {
      id,
      agent: first.agent,
      tenant: first.tenant,
      state: waiting
        ? "waiting"
        : running
          ? "active"
          : i % 4 === 3
            ? "closed"
            : "idle",
      turns: turns.length,
      started_at: first.started_at,
      last_activity_at: runs[0].started_at,
      usage,
      cost: runs.some((r) => r.cost === null)
        ? null
        : runs.reduce((a, r) => a + (r.cost ?? 0), 0),
      turn_list: turns,
      timeline: timeline.sort((a, b) => a.at.localeCompare(b.at)),
    }
  })
})()

function spansFor(run: Run): Span[] {
  const root: Span = {
    id: hex(16),
    parent_id: null,
    name: `invoke_agent ${run.agent}`,
    kind: "run",
    offset_ms: 0,
    duration_ms:
      run.duration_ms ??
      run.steps.reduce((a, s) => a + (s.duration_ms ?? 0), 0),
    error: run.status === "failed",
    attributes: {
      "gen_ai.agent.name": run.agent,
      "weft.run.id": run.id,
      "weft.agent.version": run.agent_version,
      "weftbase.tenant": run.tenant,
    },
  }
  const spans: Span[] = [root]
  for (const s of run.steps) {
    if (s.kind === "approval") continue
    const sp: Span = {
      id: hex(16),
      parent_id: root.id,
      name:
        s.kind === "model"
          ? `chat ${s.name.split("/")[1]}`
          : `execute_tool ${s.name}`,
      kind: s.kind === "model" ? "model" : "tool",
      offset_ms: s.offset_ms,
      duration_ms: s.duration_ms ?? 0,
      error: s.status === "error",
      attributes:
        s.kind === "model"
          ? {
              "gen_ai.request.model": s.name,
              "gen_ai.usage.input_tokens": s.usage?.input_tokens ?? 0,
              "gen_ai.usage.output_tokens": s.usage?.output_tokens ?? 0,
            }
          : { "gen_ai.tool.name": s.name },
    }
    spans.push(sp)
    if (s.kind === "model") {
      spans.push({
        id: hex(16),
        parent_id: sp.id,
        name: "POST /v1/messages",
        kind: "http",
        offset_ms: s.offset_ms + 10,
        duration_ms: Math.max(0, (s.duration_ms ?? 0) - 20),
        error: s.status === "error",
        attributes: {
          "http.response.status_code": s.status === "error" ? 429 : 200,
          "server.address": "[Provider host]",
        },
      })
    }
  }
  return spans
}

export const TRACES: Trace[] = [
  ...RUNS.slice(0, 24).map((r): Trace => {
    const spans = spansFor(r)
    return {
      id: r.trace_id,
      root: spans[0].name,
      service: "weftbase",
      source: "weft",
      span_count: spans.length,
      started_at: r.started_at,
      duration_ms: spans[0].duration_ms,
      error: r.status === "failed",
      run_id: r.id,
      spans,
    }
  }),
  ...[0, 1, 2].map((k): Trace => {
    const id = hex(32)
    const rootId = hex(16)
    const spans: Span[] = [
      {
        id: rootId,
        parent_id: null,
        name: "chat gpt-5-mini",
        kind: "model",
        offset_ms: 0,
        duration_ms: 1800 + k * 300,
        error: false,
        attributes: {
          "gen_ai.system": "openai",
          "gen_ai.request.model": "gpt-5-mini",
          "gen_ai.usage.input_tokens": 812,
          "gen_ai.usage.output_tokens": 164,
        },
      },
      {
        id: hex(16),
        parent_id: rootId,
        name: "POST /v1/chat/completions",
        kind: "http",
        offset_ms: 12,
        duration_ms: 1760 + k * 300,
        error: false,
        attributes: { "http.response.status_code": 200 },
      },
    ]
    return {
      id,
      root: spans[0].name,
      service: "[Service name]",
      source: "genai",
      span_count: spans.length,
      started_at: iso((k + 1) * 37 * MIN),
      duration_ms: spans[0].duration_ms,
      error: false,
      run_id: null,
      spans,
    }
  }),
].sort((a, b) => b.started_at.localeCompare(a.started_at))

export { PRICES }
export const MOCK_NOW = NOW
