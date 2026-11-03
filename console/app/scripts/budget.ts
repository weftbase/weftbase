// The size budget: the whole ../dist, each file gzipped, must stay at or
// under 800 KiB (WEFTBASE-ARCHITECTURE §7.1). `bun run budget` after a build.

import { readFileSync, readdirSync, statSync } from "node:fs"
import { join, relative, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import { gzipSync } from "node:zlib"

export const BUDGET_KIB = 800

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f)
    return statSync(p).isDirectory() ? files(p) : [p]
  })
}

const dist = resolve(fileURLToPath(import.meta.url), "../../../dist")
const sizes = files(dist).map((p) => ({
  p: relative(dist, p),
  raw: statSync(p).size,
  gz: gzipSync(readFileSync(p), { level: 9 }).length,
}))
const raw = sizes.reduce((a, s) => a + s.raw, 0)
const gz = sizes.reduce((a, s) => a + s.gz, 0)
for (const s of sizes.sort((a, b) => b.gz - a.gz).slice(0, 5)) {
  console.log(`${(s.gz / 1024).toFixed(1).padStart(7)} KiB gz  ${s.p}`)
}
console.log(
  `dist: ${files(dist).length} files, ${(raw / 1024).toFixed(1)} KiB raw, ${(gz / 1024).toFixed(1)} KiB gzip (budget ${BUDGET_KIB} KiB)`
)
if (gz > BUDGET_KIB * 1024) {
  console.error("dist is over the gzip budget")
  process.exit(1)
}
