import { describe, expect, it } from "vitest"

import {
  ago,
  between,
  cost,
  duration,
  percent,
  shortId,
  tokens,
} from "@/lib/format"

describe("format", () => {
  it("never prints a made-up zero for an unknown cost", () => {
    expect(cost({ cost_usd: null, cost_known: false })).toBe("unknown")
    expect(cost(null)).toBe("—")
    expect(cost({ cost_usd: 0, cost_known: true })).toBe("$0")
    expect(cost({ cost_usd: 0.0042, cost_known: true })).toBe("$0.0042")
    expect(cost({ cost_usd: 1.5, cost_known: true })).toBe("$1.50")
    expect(
      cost({
        cost_usd: null,
        cost_known: false,
        cost_partial: { usd: 0.5, priced: 1, of: 2 },
      })
    ).toBe("≥ $0.50")
  })
  it("writes compact durations", () => {
    expect(duration(4200)).toBe("4.20s")
    expect(duration(42_000)).toBe("42.0s")
    expect(duration(820)).toBe("820ms")
    expect(duration(124_000)).toBe("2m 04s")
    expect(duration(null)).toBe("—")
    expect(duration(undefined)).toBe("—")
  })
  it("measures an unfinished run to now", () => {
    const start = "2020-01-01T09:00:00Z"
    expect(between(start, "2020-01-01T09:00:02Z")).toBe(2000)
    expect(between(start, null, Date.parse(start) + 500)).toBe(500)
    expect(between(undefined, null)).toBeUndefined()
  })
  it("compacts tokens and keeps unknown as a dash", () => {
    expect(tokens(842)).toBe("842")
    expect(tokens(12_400)).toBe("12.4k")
    expect(tokens(3_100_000)).toBe("3.1M")
    expect(tokens(undefined)).toBe("—")
  })
  it("writes ratios, ages and short ids", () => {
    expect(percent(null)).toBe("—")
    expect(percent(0.042)).toBe("4.2%")
    expect(percent(1)).toBe("100%")
    expect(
      ago("2020-01-01T09:00:00Z", Date.parse("2020-01-01T09:05:00Z"))
    ).toBe("5m ago")
    expect(shortId("s_01M72G531Y40SM9GHSH5HGG2VC-t2")).toBe("…GG2VC-t2")
    expect(shortId("r_ok")).toBe("r_ok")
  })
})
