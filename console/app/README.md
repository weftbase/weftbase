# Weftbase Console

The operator UI for Weftbase: a TanStack Start SPA (no SSR, the shell is
prerendered), TanStack Router file routes, TanStack Query, shadcn on the
radix base, Bun, TypeScript strict. The build goes to `../dist`, which
`console/console.go` embeds. Plan: WEFTBASE-ARCHITECTURE §7.

Created with:

```sh
bunx --bun shadcn@latest init --preset b7BFgTjg8 --base radix --template start --pointer
```

## Commands

```sh
bun install
bun run dev        # Vite on :3000; proxies /v1 and /otlp to weftbase dev on :7331
bun run build      # SPA build, then scripts/clean-dist.ts writes ../dist (deterministic, with csp.json)
bun run typecheck
bun run lint       # also: no fetch outside src/lib/api, no colour literals, no server functions
bun run test       # Vitest: routes, no token in URLs, vocabulary, formats
bun run vocab      # retired words: Studio, panel, playground, devtools
bun run budget     # ../dist must stay at or under 800 KiB gzip
```

## Layout

- `src/styles.css`: the Weftbase design system theme (tokens for light and
  dark, Keycap · lit utilities, brand radii and elevations). The only place
  colour values live.
- `src/components/ui/`: shadcn components, restyled to the design system;
  `button`, `badge`, `kbd` and `weftbase-logo` come from the Weftbase
  registry item.
- `src/components/console/`: the shell, command menu, tables, timeline and
  other Console pieces.
- `src/lib/api/`: the API client layer. `client.ts` is the interface,
  `mock.ts` serves typed mock data (the default until `/v1` exists),
  `http.ts` is the `/v1` transport and the only file that calls `fetch`.
  Set `VITE_WEFTBASE_API=http` to use the server.
- `src/lib/search.ts`: URL search schemas; every view and selection is a URL.
- Fonts (Geist, Geist Mono) are self-hosted from `public/fonts`.
