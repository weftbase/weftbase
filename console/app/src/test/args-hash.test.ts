// args_sha256 in the mock data is the real SHA-256 of the canonical args.
import { describe, expect, it } from "vitest"
import { argsSha256, canonicalJSON, sha256Hex } from "@/lib/api/args-hash"
import { APPROVALS } from "@/lib/api/mock-data"

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
  })

  it("every mock approval's args_sha256 is the hash of its args", () => {
    expect(APPROVALS.length).toBeGreaterThan(0)
    for (const a of APPROVALS) expect(a.args_sha256).toBe(argsSha256(a.args))
    const refund = APPROVALS.find(
      (a) => a.tool === "issue_refund" && a.status === "pending"
    )
    expect(refund?.args).toMatchObject({ amount: 129, currency: "USD" })
    expect(refund?.args_sha256).toBe(
      "5556f7b7b36a557bedbf098588cfba35fe883c99b272dfaadff37c800fb3814e"
    )
  })
})
