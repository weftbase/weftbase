// Fixtures are the server's own answers. A copy of a golden must equal
// it byte for byte (refresh with `cp`, never edit), so the Console's
// tests fail when the server's answer changes. A capture from the demo
// (a body no golden has yet) is listed as one; every fixture is one or
// the other.
import { readFileSync, readdirSync } from "node:fs"
import { join } from "node:path"

import { describe, expect, it } from "vitest"

const FIXTURES = join(__dirname, "fixtures")
const GOLDENS = join(__dirname, "../../../../api/http/v1/testdata/v1")

/** fixture → the golden it copies. */
const COPIES: Record<string, string> = {
  "compare-diff-replay.json": "diff-replay.golden.json",
  "events-ok.json": "events-ok.golden.json",
  "meta.json": "meta.golden.json",
  "run-children-holes.json": "run-children-holes.golden.json",
  "run-fail.json": "run-fail.golden.json",
  "runs.json": "runs.golden.json",
  "search.json": "search.golden.json",
  "session-orders.json": "session-orders.golden.json",
  "session-timeline.json": "session-timeline.golden.json",
  "sessions.json": "sessions.golden.json",
  "settings-pricing.json": "settings-pricing.golden.json",
  "spans-sub.json": "spans-sub.golden.json",
  "spans-tree.json": "spans-tree.golden.json",
  "stats-agent.json": "stats-agent.golden.json",
  "stats-tool.json": "stats-tool.golden.json",
  "stats-total.json": "stats-total.golden.json",
  "step-1.json": "step-1.golden.json",
  "trace.json": "trace.golden.json",
  "traces.json": "traces.golden.json",
  "transcript-as-of-step.json": "transcript-as-of-step.golden.json",
  "transcript-ok.json": "transcript-ok.golden.json",
}

/** Bodies captured from `go run ./examples/demo`, for routes or cases
 * no golden covers (the inventory, approvals, conversations, chains). */
const CAPTURES = new Set([
  "agent-detail.json",
  "agents-summary.json",
  "approvals.json",
  "compare-diff-failed.json",
  "compare-diff-refunds.json",
  "compare-diff-resume.json",
  "compare-diff-same.json",
  "compare-spans-support.json",
  "conversation-desk.json",
  "conversation-failed.json",
  "conversation-refunds.json",
  "conversation-waiting.json",
  "runs-chains.json",
  "runs-outcomes.json",
  "sessions-demo.json",
  "stats-agent-outcomes.json",
  "tools.json",
])

describe("fixtures", () => {
  it("are each a copy of a golden or a declared capture", () => {
    const loose = readdirSync(FIXTURES).filter(
      (f) => f.endsWith(".json") && !(f in COPIES) && !CAPTURES.has(f)
    )
    expect(loose).toEqual([])
  })

  it.each(Object.entries(COPIES))("%s equals %s", (fixture, golden) => {
    const ours = readFileSync(join(FIXTURES, fixture), "utf8")
    const theirs = readFileSync(join(GOLDENS, golden), "utf8")
    // On failure: cp api/http/v1/testdata/v1/<golden> src/test/fixtures/<fixture>
    expect(ours === theirs, `${fixture} drifted from ${golden}`).toBe(true)
  })
})
