import { defineConfig } from "vite"
import { tanstackStart } from "@tanstack/react-start/plugin/vite"
import viteReact from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

// The Console is a static SPA (WEFTBASE-ARCHITECTURE §7.1, §7.3): no SSR
// at runtime and no server functions. The build runs the server bundle
// once to prerender the shell; scripts/clean-dist.ts then copies the
// static client into ../dist, the tree console.go embeds.
//
// Relative asset URLs: the Go handler writes one <base href> per request,
// so the same bundle mounts at any prefix.
const API = process.env.WEFTBASE_API ?? "http://127.0.0.1:7331"

const config = defineConfig(({ command }) => ({
  resolve: { tsconfigPaths: true },
  // Relative in the build only: the dev server serves modules from "/".
  base: command === "build" ? "./" : "/",
  build: { sourcemap: false },
  // `weftbase dev` serves the API on :7331. The proxy keeps the browser
  // same-origin, so the session cookie and CSRF rules are the real ones.
  // WEFTBASE_API points the proxy at a server on another port.
  server: {
    proxy: {
      "/v1": { target: API, changeOrigin: false },
      "/otlp": { target: API, changeOrigin: false },
    },
  },
  plugins: [
    tailwindcss(),
    tanstackStart({
      // Asset URLs are relative ("./"), but the router's build-time
      // basepath stays "/": the runtime mount comes from <base href>
      // through the router's rewrite (src/router.tsx).
      router: { basepath: "/" },
      spa: {
        enabled: true,
        prerender: { outputPath: "index.html" },
      },
    }),
    viteReact(),
  ],
}))

export default config
