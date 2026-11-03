import { describe, expect, it } from "vitest"

import { cost, duration } from "@/lib/format"

describe("format", () => {
  it("never prints $0.00 for an unknown cost", () => {
    expect(cost(null)).toBe("unknown")
    expect(cost(0.0042)).toBe("$0.0042")
    expect(cost(1.5)).toBe("$1.50")
  })
  it("writes durations like the artboard", () => {
    expect(duration(4200)).toBe("4.2 s")
    expect(duration(820)).toBe("820 ms")
    expect(duration(null)).toBe("—")
  })
})
