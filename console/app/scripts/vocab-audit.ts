// The vocabulary audit: no retired word reaches the Console's source.
// "Studio", "panel", "playground" and "devtools" name the old product and
// its parts; the Console says runs, sessions, approvals, agents, traces.
// It scans every .ts and .tsx file under src/ (tests excluded: they name
// the words to prove they are gone). `bun run vocab` fails on a hit.

import { readFileSync, readdirSync, statSync } from "node:fs"
import { join, relative, resolve } from "node:path"
import { fileURLToPath } from "node:url"

export const RETIRED = [
  "studio",
  "panel",
  "playground",
  "devtools",
  "dev tools",
] as const

const WORD = new RegExp(
  `\\b(${RETIRED.map((w) => w.replace(" ", "\\s+")).join("|")})\\b`,
  "i"
)

export interface Hit {
  file: string
  line: number
  word: string
  text: string
}

/** scan returns every retired word in one file's text. */
export function scan(file: string, text: string): Hit[] {
  const hits: Hit[] = []
  text.split("\n").forEach((line, i) => {
    const m = WORD.exec(line)
    if (m)
      hits.push({
        file,
        line: i + 1,
        word: m[1].toLowerCase(),
        text: line.trim(),
      })
  })
  return hits
}

/** sources lists the files the audit reads. */
export function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f)
    if (statSync(p).isDirectory()) return f === "test" ? [] : sources(p)
    return /\.tsx?$/.test(f) && !/\.test\.tsx?$/.test(f) ? [p] : []
  })
}

export function audit(src: string): Hit[] {
  return sources(src).flatMap((p) =>
    scan(relative(src, p), readFileSync(p, "utf8"))
  )
}

if ((import.meta as { main?: boolean }).main) {
  const src = resolve(fileURLToPath(import.meta.url), "../../src")
  const hits = audit(src)
  for (const h of hits)
    console.error(
      `src/${h.file}:${h.line}: retired word "${h.word}" in: ${h.text}`
    )
  if (hits.length) process.exit(1)
  console.log("vocab: clean")
}
