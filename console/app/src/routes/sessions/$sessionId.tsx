import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Link, createFileRoute } from "@tanstack/react-router"
import * as React from "react"

import { CopyId } from "@/components/console/copy-id"
import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { Page, PageHeader } from "@/components/console/page-header"
import { useNow } from "@/components/console/relative-time"
import { MarkerLine } from "@/components/console/session/marker"
import {
  buildItems,
  sessionTitle,
  turnNumber,
} from "@/components/console/session/model"
import { SessionRail } from "@/components/console/session/rail"
import { SessionMenu } from "@/components/console/session/session-menu"
import { TurnBlock } from "@/components/console/session/turn"
import { LivePill, StatusBadge } from "@/components/console/status"
import { Button } from "@/components/ui/button"
import { useSessionLive } from "@/lib/api/live"
import { conversationKey, q } from "@/lib/api/queries"
import { TURNS_PAGE, loadEarlier, mergeEarlier } from "@/lib/api/sessions"
import type { ConversationView } from "@/lib/api/sessions"
import { sessionStatus } from "@/lib/api/status"
import { count } from "@/lib/format"
import { useSetSearch, validateSession } from "@/lib/search"
import type { SessionSearch } from "@/lib/search"

// A session: ONE conversation, read top to bottom. Per turn the user's
// message, one collapsed line of what the agent did (it opens that
// turn's waterfall in place) and the agent's reply; what happened on
// the way (a wait on a person, a compaction, a fork) is a one-line
// marker where it happened. The session's facts are in a slim rail.
// `?turn=` is the turn whose waterfall is open.
export const Route = createFileRoute("/sessions/$sessionId")({
  validateSearch: validateSession,
  head: () => ({ meta: [{ title: "Session · weftbase" }] }),
  component: SessionPage,
})

function SessionPage() {
  const { sessionId } = Route.useParams()
  // A new session is a new page: nothing of the last one is kept.
  return <Session key={sessionId} sessionId={sessionId} />
}

function Session({ sessionId }: { sessionId: string }) {
  const search = Route.useSearch()
  const set = useSetSearch<SessionSearch>(Route.fullPath)
  const qc = useQueryClient()
  const stream = useSessionLive(sessionId)
  const now = useNow()

  // One list with a live window at its end: the page opens on the newest
  // turns, and a live tick reads those again and nothing else. "Earlier
  // turns" are read once and put at the top of the same cached list;
  // they are finished turns, so no tick reads them again.
  // The conversation carries the session's own row too (the rail's
  // facts), so the page is one request, and one per tick.
  const conversation = useQuery(q.conversationWindow(sessionId))
  const view = conversation.data
  const earlier = useMutation({
    mutationFn: ({ from, to }: { from: number; to: number }) =>
      loadEarlier(sessionId, from, to),
    onSuccess: (pages) =>
      qc.setQueryData<ConversationView>(conversationKey(sessionId), (v) =>
        v ? mergeEarlier(v, pages) : v
      ),
  })
  const readBackTo = (from: number) => {
    if (!view || earlier.isPending || from >= view.offset) return
    earlier.mutate({ from: Math.max(0, from), to: view.offset })
  }

  const items = React.useMemo(
    () => (view ? buildItems(view, Math.max(now, Date.now())) : []),
    [view, now]
  )

  // A link to a turn that is not loaded: read back to it.
  const wanted = search.turn
  const has =
    wanted === undefined ||
    items.some(
      (i) => i.type === "turn" && turnNumber(i.turn, i.ordinal) === wanted
    )
  const reading = earlier.isPending
  const stuck = earlier.isError
  const offset = view?.offset
  const readBack = React.useEffectEvent(readBackTo)
  React.useEffect(() => {
    if (has || offset === undefined || offset === 0 || reading || stuck) return
    readBack(Math.min(offset - TURNS_PAGE, (wanted ?? 1) - 1))
  }, [has, offset, reading, stuck, wanted])

  // Open at the newest turn (or the linked one), and stay at the end
  // while new turns arrive, unless the reader scrolled up.
  const scroller = React.useRef<HTMLDivElement>(null)
  const atEnd = React.useRef(true)
  const placed = React.useRef(false)
  const turnCount = view?.turns.length ?? 0
  const lastStatus = view?.turns.at(-1)?.display_status
  React.useLayoutEffect(() => {
    const el = scroller.current
    if (!el || !view) return
    if (!placed.current) {
      if (wanted !== undefined) {
        const target = el.querySelector(`#turn-${wanted}`)
        if (!target) return // still reading back to it
        target.scrollIntoView({ block: "start" })
        atEnd.current = false
      } else el.scrollTop = el.scrollHeight
      placed.current = true
      return
    }
    if (atEnd.current) el.scrollTop = el.scrollHeight
    // Only when a turn arrives or the newest one changes state.
  }, [view === undefined, turnCount, lastStatus])

  const s = view?.session
  const title = sessionTitle(
    s ??
      (view
        ? { title: view.title, first_prompt: view.turns[0]?.input }
        : undefined)
  )
  const total = view?.totalTurns
  const error = conversation.error

  return (
    <Page
      flush
      className="flex overflow-hidden"
      header={
        <PageHeader
          title={
            <span className="flex min-w-0 items-center gap-1.5">
              <Link
                to="/sessions"
                className="font-normal text-muted-foreground hover:text-foreground"
              >
                Sessions
              </Link>
              <span className="font-normal text-subtle-foreground">›</span>
              <span className="min-w-0 truncate">{title}</span>
            </span>
          }
          status={
            (s ?? view) && <StatusBadge state={sessionStatus((s ?? view)!)} />
          }
          id={<CopyId id={sessionId} label="session id" />}
          meta={
            // The pill appears once the stream is open: a quiet session's
            // stream answers with its first frame, which can take a while.
            stream !== "connecting" && <LivePill state={stream} />
          }
          actions={view && <SessionMenu sessionId={sessionId} turns={total} />}
        />
      }
    >
      {error && !view ? (
        <div className="flex-1">
          <ErrorState
            error={error}
            what="The session"
            onRetry={() => void conversation.refetch()}
          />
        </div>
      ) : !view ? (
        <div className="flex-1">
          <LoadingRows rows={8} />
        </div>
      ) : (
        <>
          <div
            ref={scroller}
            onScroll={(e) => {
              const el = e.currentTarget
              atEnd.current =
                el.scrollHeight - el.scrollTop - el.clientHeight < 48
            }}
            onKeyDown={(e) => {
              // ↑/↓ or j/k move between the turns' summary lines.
              if (e.metaKey || e.ctrlKey || e.altKey) return
              const down = e.key === "ArrowDown" || e.key === "j"
              const up = e.key === "ArrowUp" || e.key === "k"
              const at = (e.target as HTMLElement).closest("[data-turn-line]")
              if ((!down && !up) || !at) return
              const all = Array.from(
                e.currentTarget.querySelectorAll<HTMLElement>(
                  "[data-turn-line]"
                )
              )
              const next = all[all.indexOf(at as HTMLElement) + (down ? 1 : -1)]
              if (!next) return
              e.preventDefault()
              next.focus()
            }}
            className="min-w-0 flex-1 overflow-auto"
          >
            {/* Too narrow for the rail beside the conversation: its facts
                are a block that opens above it. */}
            {s && (
              <details className="hidden border-b text-xs max-[900px]:block">
                <summary className="flex h-8 cursor-pointer items-center px-4 font-medium text-muted-foreground select-none hover:text-foreground">
                  Session facts
                </summary>
                <SessionRail
                  session={s}
                  delegated={view.delegated}
                  newest={view.turns.at(-1)}
                  className="border-t"
                />
              </details>
            )}
            <div className="mx-auto flex w-full max-w-[840px] flex-col gap-4 px-4 pt-4 pb-10">
              {view.marksCapped && (
                <p className="text-xs text-muted-foreground">
                  This session has more marks than one answer reads: the markers
                  shown are its first ones only.
                </p>
              )}
              {items.length === 0 && (
                <EmptyState title="No turns yet">
                  The conversation appears here with the session&apos;s first
                  run.
                </EmptyState>
              )}
              {items.map((item) => {
                switch (item.type) {
                  case "older":
                    return (
                      <div
                        key={item.key}
                        className="flex items-center justify-center gap-2 text-xs text-muted-foreground"
                      >
                        <Button
                          size="sm"
                          variant="secondary"
                          disabled={earlier.isPending}
                          onClick={() => readBackTo(view.offset - TURNS_PAGE)}
                        >
                          {earlier.isPending
                            ? "Loading…"
                            : `Show ${count(Math.min(TURNS_PAGE, item.count))} earlier ${Math.min(TURNS_PAGE, item.count) === 1 ? "turn" : "turns"}`}
                        </Button>
                        <span>
                          {earlier.isError
                            ? "The earlier turns could not be read."
                            : `${count(item.count)} of ${count(view.totalTurns)} not shown`}
                        </span>
                      </div>
                    )
                  case "gap":
                    return (
                      <p
                        key={item.key}
                        className="pl-9 text-xs text-subtle-foreground"
                      >
                        {item.text}
                      </p>
                    )
                  case "mark":
                    return (
                      <div key={item.key} className="pl-9">
                        <MarkerLine marker={item.marker} />
                      </div>
                    )
                  case "turn": {
                    const n = turnNumber(item.turn, item.ordinal)
                    return (
                      <TurnBlock
                        key={item.key}
                        item={item}
                        agent={view.agent}
                        open={search.turn === n}
                        onToggle={() =>
                          set({
                            turn: search.turn === n ? undefined : n,
                            span: undefined,
                          })
                        }
                      />
                    )
                  }
                }
              })}
            </div>
          </div>
          {s && (
            <SessionRail
              session={s}
              delegated={view.delegated}
              newest={view.turns.at(-1)}
              className="w-[272px] flex-none overflow-auto border-l max-[900px]:hidden"
            />
          )}
        </>
      )}
    </Page>
  )
}
