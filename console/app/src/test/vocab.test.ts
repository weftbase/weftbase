// The vocabulary audit: the Console never says "Studio", "panel",
// "playground" or "devtools", and the scanner really catches them.
import { resolve } from "node:path"
import { describe, expect, it } from "vitest"

import { RETIRED, audit, scan } from "../../scripts/vocab-audit"

describe("vocabulary audit", () => {
  it("finds no retired word in src/", () => {
    expect(audit(resolve(__dirname, ".."))).toEqual([])
  })

  it.each(RETIRED)("catches %s", (word) => {
    const hits = scan("x.tsx", `<p>Open the ${word.toUpperCase()} view</p>`)
    expect(hits).toHaveLength(1)
  })

  it("leaves words that only contain a retired one", () => {
    expect(scan("x.tsx", 'role="tabpanel" studios panels')).toEqual([])
  })
})
