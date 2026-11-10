// The canonical-JSON SHA-256 an approval's args_sha256 is checked with.
import { describe, expect, it } from "vitest"
import { argsSha256, canonicalJSON, sha256Hex } from "@/lib/api/args-hash"

describe("args-hash", () => {
  it("matches the SHA-256 test vectors", () => {
    expect(sha256Hex("")).toBe(
      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    )
    expect(sha256Hex("abc")).toBe(
      "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
    )
  })

  it("sorts keys and drops whitespace", () => {
    expect(canonicalJSON({ b: 1, a: { d: [1, "x"], c: null } })).toBe(
      '{"a":{"c":null,"d":[1,"x"]},"b":1}'
    )
    expect(argsSha256({ b: 1, a: 2 })).toBe(argsSha256({ a: 2, b: 1 }))
  })
})
