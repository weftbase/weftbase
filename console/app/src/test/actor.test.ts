// Who did something, said once and the same way on every screen.
import { describe, expect, it } from "vitest"

import { actorText } from "@/lib/actor"

describe("actorText", () => {
  const me = { kind: "operator", id: "operator" }

  it("says you for the signed-in principal", () => {
    expect(actorText({ kind: "operator", id: "operator" }, me)).toBe("you")
    const key = { kind: "key", id: "key_01M72SZH8MCMM23A2A16X09TVV" }
    expect(actorText(key, key)).toBe("you")
    // The same id under another kind is someone else.
    expect(actorText({ kind: "key", id: "operator" }, me)).toBe("API key …ator")
  })

  it("names an operator and a key once", () => {
    expect(actorText({ kind: "operator", id: "op_maria" }, me)).toBe(
      "operator op_maria"
    )
    // The Console's operator has no name of its own: not "operator operator".
    expect(actorText({ kind: "operator", id: "operator" })).toBe("an operator")
    expect(
      actorText({ kind: "key", id: "key_01M72SZH8MCMM23A2A16X04HH1" }, me)
    ).toBe("API key …4HH1")
  })

  it("names the system for what the server did itself (an expiry)", () => {
    expect(actorText({ kind: "system" })).toBe("system")
    expect(actorText({ kind: "system", id: "" }, me)).toBe("system")
    expect(actorText("system")).toBe("system")
  })

  it("reads the strings some records carry", () => {
    expect(actorText("op_maria", me)).toBe("op_maria")
    expect(actorText("operator op_maria")).toBe("operator op_maria")
    expect(actorText("key key_01M72SZH8MCMM23A2A16X04HH1")).toBe(
      "API key …4HH1"
    )
    expect(actorText("operator operator", me)).toBe("you")
  })

  it("names nobody when nobody is named", () => {
    expect(actorText(undefined)).toBeUndefined()
    expect(actorText(null, me)).toBeUndefined()
    expect(actorText({ kind: "operator" })).toBeUndefined()
    expect(actorText("")).toBeUndefined()
  })
})
