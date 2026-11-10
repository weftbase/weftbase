// The top bar's "New run" action (CONSOLE-UX, New run): pick an agent and
// a version or label, type the input, Start; it calls api.admit
// (POST /v1/runs) and opens the new run's page, live.
//
// This is its slot in the shell. It renders nothing until the action is
// built: a button that cannot start a run would be a fake control. Build
// it here (a Button size="sm" opening a Dialog) and the top bar picks it
// up; the agent page reuses the same component with its agent preset.
export function NewRun(_props: { agent?: string }) {
  return null
}
