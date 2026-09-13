import { useInfiniteQuery } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { profilePlaysQuery } from "@/api/queries";
import type { NowSession, PlayRow } from "@/api/types";
import { fmtDuration } from "@/lib/format";
import { Art } from "./Art";
import { Panel } from "./Panel";
import { Skeleton } from "./Skeleton";

function LiveRow({ session }: { session: NowSession }) {
  const title = session.series
    ? `${session.series} · ${session.season_episode} — ${session.title}`
    : session.title;
  return (
    <div className="flex items-center gap-3 bg-mote/10 px-3 py-2">
      <Art
        src={`/api/art/item/${session.item_id}`}
        kind="poster"
        fallback={session.title}
        className="h-[44px] w-[30px] shrink-0"
      />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] text-ink">{title}</div>
        <div className="truncate text-[11.5px] text-muted">{session.user}</div>
      </div>
      <span className="shrink-0 rounded-full bg-mote/20 px-2 py-0.5 text-[10.5px] font-medium text-mote">
        live
      </span>
    </div>
  );
}

// Episode plays carry no season/episode number on the spine (only the
// episode's own title and its series' name), so this reads "Series — Title"
// rather than the "Series · S01E04 — Title" form the live row can manage
// with NowSession.season_episode.
function playTitle(p: PlayRow): string {
  return p.series_name ? `${p.series_name} — ${p.name}` : p.name;
}

function PlayItem({ play }: { play: PlayRow }) {
  return (
    <div className="flex items-center gap-3 px-3 py-2">
      <Art
        src={`/api/art/item/${play.item_id}`}
        kind="poster"
        fallback={play.name}
        className="h-[44px] w-[30px] shrink-0"
      />
      <div className="min-w-0 flex-1">
        {play.jf_url ? (
          <a
            href={play.jf_url}
            target="_blank"
            rel="noreferrer"
            className="block truncate border-b border-cyan text-[13px] text-ink"
          >
            {playTitle(play)}
          </a>
        ) : (
          <div className="truncate text-[13px] text-ink">{playTitle(play)}</div>
        )}
        <div className="truncate text-[11.5px] text-muted">{play.item_type}</div>
      </div>
      <div className="shrink-0 text-right font-mono text-[11.5px] text-muted">
        <div>{play.at.slice(0, 10)}</div>
        <div className="text-ink">
          {play.played && <span className="text-cyan">✓ </span>}
          {fmtDuration(play.play_duration_sec)}
        </div>
      </div>
    </div>
  );
}

export function PlayList({
  userId,
  library,
  nowPlaying,
}: {
  userId: string;
  library: string;
  nowPlaying?: NowSession | null;
}) {
  const q = useInfiniteQuery(profilePlaysQuery(userId, library));
  const sentinelRef = useRef<HTMLDivElement>(null);

  // q.fetchNextPage/hasNextPage/isFetchingNextPage can all be new references
  // on a render that has nothing to do with paging, which would otherwise
  // tear down and rebuild the observer on every such render. Mount once and
  // read the current query state through a ref instead.
  const stateRef = useRef(q);
  stateRef.current = q;

  // The sentinel div doesn't exist yet during the isPending skeleton, so the
  // effect has to re-run once loading finishes to find it -- isPending makes
  // that one transition without churning on every paging render the way
  // hasNextPage/isFetchingNextPage/fetchNextPage identity would.
  // biome-ignore lint/correctness/useExhaustiveDependencies: isPending is a re-run trigger, not a value the effect body reads
  useEffect(() => {
    const el = sentinelRef.current;
    if (!el) return;
    const obs = new IntersectionObserver((entries) => {
      const { hasNextPage, isFetchingNextPage, fetchNextPage } = stateRef.current;
      if (entries[0]?.isIntersecting && hasNextPage && !isFetchingNextPage) {
        void fetchNextPage();
      }
    });
    obs.observe(el);
    return () => obs.disconnect();
  }, [q.isPending]);

  if (q.isPending) {
    return <Skeleton className="h-[420px] w-full" />;
  }
  if (q.isError) {
    return (
      <Panel className="p-4">
        <p className="text-[13px] text-muted">{(q.error as Error).message}</p>
      </Panel>
    );
  }

  const plays = q.data.pages.flatMap((page) => page.data.plays);

  return (
    <Panel className="flex flex-col divide-y divide-line/50 p-1.5">
      {nowPlaying && <LiveRow session={nowPlaying} />}
      {plays.length === 0 && !nowPlaying ? (
        <p className="px-3 py-8 text-center text-[13px] text-muted">No plays recorded yet.</p>
      ) : (
        // (at, item_id) is the spine's own play identity for one user (see
        // spine.go), so it's a stable key without falling back to index.
        plays.map((p) => <PlayItem key={`${p.item_id}-${p.at}`} play={p} />)
      )}
      <div ref={sentinelRef} className="h-px" />
    </Panel>
  );
}
