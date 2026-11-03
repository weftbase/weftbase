// Copies the static client build into ../dist, the tree console.go
// embeds (//go:embed all:dist). ../dist is rebuilt from scratch on every
// build, so the committed tree is exactly this script's output and
// `make console-check` can diff it byte for byte.
//
// On the way it:
// - drops the server bundle and prerender scratch (the SPA build runs the
//   server once to render the shell) and sourcemaps;
// - makes the shell's asset URLs relative ("/./assets/…" → "./assets/…")
//   and puts one <base href="/"> first in <head>; the Go handler rewrites
//   that href per request for a mount prefix;
// - pins the router's prerender timestamp so two builds are identical;
// - writes csp.json with the sha256 of every inline script, so the
//   handler can set a strict CSP without parsing the shell at startup.

import { createHash } from "node:crypto"
import { cp, readFile, rm, writeFile } from "node:fs/promises"

const src = new URL("../dist/client", import.meta.url).pathname
const dst = new URL("../../dist", import.meta.url).pathname

await rm(dst, { recursive: true, force: true })
await cp(src, dst, {
  recursive: true,
  filter: (path) => !path.endsWith(".map"),
})

const shellPath = `${dst}/index.html`
let shell = await readFile(shellPath, "utf8")
shell = shell.replaceAll('"/./', '"./')
shell = shell.replace(/<base href="[^"]*"\s*\/?>/, "")
shell = shell.replace("<head>", '<head><base href="/">')
shell = shell.replace(/([,{[]u:)\d{12,}(,s:)/g, "$10$2")
await writeFile(shellPath, shell)

const hashes = [
  ...shell.matchAll(/<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/g),
]
  .map((m) => m[1])
  .filter((body) => body.length > 0)
  .map(
    (body) => `'sha256-${createHash("sha256").update(body).digest("base64")}'`
  )
const csp = {
  "default-src": ["'self'"],
  "script-src": ["'self'", ...hashes],
  "style-src": ["'self'", "'unsafe-inline'"],
  "img-src": ["'self'", "data:"],
  "font-src": ["'self'"],
  "connect-src": ["'self'"],
  "frame-ancestors": ["'none'"],
  "base-uri": ["'self'"],
  "form-action": ["'self'"],
}
await writeFile(`${dst}/csp.json`, JSON.stringify(csp, null, 2) + "\n")
console.log(
  `console: static client copied to ${dst} (${hashes.length} inline script hashes in csp.json)`
)
