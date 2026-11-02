// The one version (B6): the weft framework module's tag, read from the
// Go source of its version/version.go so nothing in the web build
// carries a second copy. The framework is another module
// (github.com/weftbase/weft): `go list -m` finds the source this
// build resolves — the go.work checkout in development, the module
// cache for a tagged weft — exactly as the Go side's version.Version
// does. vite.panel.config.ts stamps it into panel.js as
// __PANEL_STUDIO_VERSION__; the panel's version suite compares the
// "Studio is newer" guard against the same string.
import { execFileSync } from "node:child_process"
import { readFileSync } from "node:fs"
import path from "node:path"
import { fileURLToPath } from "node:url"

const weftModule = "github.com/weftbase/weft"
const weftCoreModule = "github.com/weftbase/weft/core"

/** The directory of a weft module (the root by default), as `go list
 * -m` resolves it from the repository root (the weftbase module,
 * which requires weft; studio/web's own go.mod is a guard that
 * requires nothing): the tests that read weft's docs and Go
 * source (the glossary's anchors, core's constants) use it too. */
export function weftModuleDir(module: string = weftModule): string {
  // A path, not a URL: under vitest's jsdom, URL is jsdom's and
  // readFileSync refuses it.
  const root = path.resolve(
    path.dirname(fileURLToPath(import.meta.url)),
    "../../.."
  )
  const dir = execFileSync("go", ["list", "-m", "-f", "{{.Dir}}", module], {
    cwd: root,
    encoding: "utf8",
  }).trim()
  if (!dir) throw new Error(`weft-version: go list -m cannot resolve ${module}`)
  return dir
}

/** A file of the weft repository by its repository path: "core/…"
 * lives in the core module, everything else in the root module. */
export function weftPath(rel: string): string {
  return rel.startsWith("core/")
    ? path.join(weftModuleDir(weftCoreModule), rel.slice("core/".length))
    : path.join(weftModuleDir(), rel)
}

export function weftVersion(): string {
  const go = readFileSync(
    path.join(weftModuleDir(), "version", "version.go"),
    "utf8"
  )
  const m = /^const Version = "([^"]+)"$/m.exec(go)
  if (!m)
    throw new Error(
      "weft-version: const Version not found in the weft module's version/version.go"
    )
  return m[1]
}
