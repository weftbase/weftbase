//  @ts-check

import { tanstackConfig } from "@tanstack/eslint-config"

// A colour written as a literal (#fff, rgb(), hsl(), oklch(), or a
// Tailwind arbitrary value such as bg-[#fff]) outside src/styles.css.
// Colours come from the Weftbase tokens only.
const COLOUR = String.raw`/#([0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})\b|\b(rgba?|hsla?|oklch|oklab)\(/`

export default [
  ...tanstackConfig,
  {
    rules: {
      "import/no-cycle": "off",
      "import/order": "off",
      "import/consistent-type-specifier-style": "off",
      "sort-imports": "off",
      "@typescript-eslint/array-type": "off",
      "@typescript-eslint/require-await": "off",
      "@typescript-eslint/no-unnecessary-condition": "off",
      "pnpm/json-enforce-catalog": "off",
    },
  },
  {
    files: ["src/**/*.{ts,tsx}"],
    ignores: ["src/**/*.test.{ts,tsx}", "src/test/**"],
    rules: {
      // Requests go through the API client layer only (§7.6).
      "no-restricted-globals": [
        "error",
        {
          name: "fetch",
          message: "Call the API through src/lib/api (the client layer).",
        },
        { name: "EventSource", message: "Streams go through src/lib/api." },
        {
          name: "XMLHttpRequest",
          message: "Call the API through src/lib/api.",
        },
      ],
      // SPA only: no server functions.
      "no-restricted-imports": [
        "error",
        {
          paths: [
            {
              name: "@tanstack/react-start",
              importNames: ["createServerFn", "createServerFileRoute"],
              message: "The Console is a static SPA: Go is the only server.",
            },
          ],
        },
      ],
      "no-restricted-syntax": [
        "error",
        {
          selector: `Literal[value=${COLOUR}]`,
          message:
            "No colour literals: use a Weftbase token (bg-muted, text-success-foreground…).",
        },
        {
          selector: `TemplateElement[value.raw=${COLOUR}]`,
          message: "No colour literals: use a Weftbase token.",
        },
      ],
    },
  },
  {
    // The transport is the one place allowed to call fetch.
    files: ["src/lib/api/http.ts"],
    rules: { "no-restricted-globals": "off" },
  },
  {
    ignores: [
      "eslint.config.js",
      ".prettierrc",
      "src/routeTree.gen.ts",
      "dist/**",
    ],
  },
]
