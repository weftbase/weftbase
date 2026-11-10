// GET /v1/stats is always grouped (`by` is required): there is no
// ungrouped total. The overview sums the groups of by=agent, which
// partition the runs.

import type { CostFields, Stats, StatsBucket } from "./api/types"

export interface StatsTotal {
  runs: number
  failed: number
  /** null when there were no runs. */
  failureRate: number | null
  inputTokens: number
  outputTokens: number
  /** The sum of the groups' costs; cost_known only if every group's is. */
  cost: Pick<CostFields, "cost_usd" | "cost_known" | "cost_partial">
  /** One bucket per stats.bucket_ms from stats.since, oldest first. */
  series: StatsBucket[]
}

export function totalOf(stats: Stats): StatsTotal {
  const series: StatsBucket[] = Array.from({ length: stats.buckets }, () => ({
    runs: 0,
    failed: 0,
    calls: 0,
    failed_calls: 0,
    input_tokens: 0,
    output_tokens: 0,
  }))
  let runs = 0
  let failed = 0
  let inputTokens = 0
  let outputTokens = 0
  let usd = 0
  let priced = 0
  for (const g of stats.groups) {
    runs += g.runs
    failed += g.failed
    inputTokens += g.input_tokens
    outputTokens += g.output_tokens
    if (g.cost_known && g.cost_usd !== null) {
      usd += g.cost_usd
      priced++
    } else if (g.cost_partial) usd += g.cost_partial.usd
    g.series.forEach((b, i) => {
      const t = series.at(i)
      if (!t) return
      t.runs += b.runs
      t.failed += b.failed
      t.calls += b.calls
      t.failed_calls += b.failed_calls
      t.input_tokens += b.input_tokens
      t.output_tokens += b.output_tokens
    })
  }
  const known = priced === stats.groups.length
  return {
    runs,
    failed,
    failureRate: runs === 0 ? null : failed / runs,
    inputTokens,
    outputTokens,
    cost: known
      ? { cost_usd: usd, cost_known: true }
      : {
          cost_usd: null,
          cost_known: false,
          cost_partial:
            priced > 0 ? { usd, priced, of: stats.groups.length } : undefined,
        },
    series,
  }
}
