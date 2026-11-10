// What the command menu does with typed text. Pure.
//
// A single token long enough to be an id is looked up as a run (exact or
// by prefix), a session and a public id; anything else is a run search.

/** The shortest text worth looking up as an id. */
export const ID_MIN = 6

/** idCandidate is the text as an id to look up, or undefined when it
 * cannot be one (it has spaces, or it is too short). */
export function idCandidate(text: string): string | undefined {
  const t = text.trim()
  if (t.length < ID_MIN || /\s/.test(t)) return undefined
  return t
}

/** matches: a case-insensitive "every word appears" filter for the
 * menu's static entries (pages, agents, actions). */
export function matches(text: string, ...fields: (string | undefined)[]) {
  const hay = fields.filter(Boolean).join(" ").toLowerCase()
  return text
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((w) => hay.includes(w))
}

/** exactFirst orders id-lookup hits: the id that was typed first, then
 * the shortest (a run before the child runs whose ids extend it). */
export function exactFirst<T extends { id: string }>(
  hits: readonly T[],
  typed: string
): T[] {
  return [...hits].sort(
    (a, b) =>
      Number(b.id === typed) - Number(a.id === typed) ||
      a.id.length - b.id.length
  )
}
