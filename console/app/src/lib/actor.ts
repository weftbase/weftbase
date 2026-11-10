// Who did something, said once and the same way everywhere: "you" for
// the signed-in operator, "operator op_maria", "API key …4HH1",
// "system". Every screen that prints an actor uses actorText.

import { useQuery } from "@tanstack/react-query"

import { q } from "./api/queries"

export interface ActorLike {
  kind?: string
  id?: string
}

/** actorText names an actor. `me` is the signed-in principal (useMe):
 * the same operator reads "you". An "operator op_1" / "key key_1"
 * string (as some records carry it) is read the same way. undefined
 * when nobody is named. */
export function actorText(
  actor: ActorLike | string | null | undefined,
  me?: ActorLike
): string | undefined {
  if (!actor) return undefined
  let a: ActorLike
  if (typeof actor === "string") {
    const [kind, ...rest] = actor.trim().split(/\s+/)
    a =
      rest.length > 0 && ["operator", "key", "system"].includes(kind)
        ? { kind, id: rest.join(" ") }
        : kind === "system"
          ? { kind }
          : { id: actor.trim() }
  } else a = actor
  if (a.kind === "system") return "system"
  if (!a.id) return undefined
  if (me?.id && me.id === a.id && (me.kind ?? "") === (a.kind ?? me.kind))
    return "you"
  if (a.kind === "key")
    return `API key …${a.id.length > 4 ? a.id.slice(-4) : a.id}`
  // An operator whose id is the word itself has no name to add.
  if (a.kind === "operator")
    return a.id === "operator" ? "an operator" : `operator ${a.id}`
  return a.id
}

/** The signed-in principal, for actorText's "you". */
export function useMe(): ActorLike | undefined {
  return useQuery(q.meta()).data?.principal
}
