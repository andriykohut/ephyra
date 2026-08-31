import { createRoute } from "@tanstack/react-router";
import type { Snapshot } from "@/api/types";
import { NowPlayingCard } from "@/components/NowPlayingCard";
import { Panel } from "@/components/Panel";
import { Skeleton } from "@/components/Skeleton";
import { fmtBitrate } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useNowPlaying } from "@/live/useNowPlaying";
import { Route as rootRoute } from "./__root";

type Conn = "connecting" | "live" | "reconnecting";

function ConnDot({ conn }: { conn: Conn }) {
  if (conn === "live") {
    return (
      <span className="flex items-center gap-1.5 text-cyan/90">
        <span
          aria-hidden
          className="h-1.5 w-1.5 rounded-full bg-cyan shadow-[0_0_6px_rgba(79,224,216,0.85)]"
        />
        live
      </span>
    );
  }
  if (conn === "reconnecting") {
    return (
      <span className="flex items-center gap-1.5 text-mote">
        <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-mote motion-safe:animate-pulse" />
        reconnecting
      </span>
    );
  }
  return (
    <span className="flex items-center gap-1.5 text-muted">
      <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-muted/60" />
      connecting
    </span>
  );
}

// Pure — the hook feeds it, tests hand it props directly. `degraded` is the
// hook's own flag, not `snapshot.degraded`: during an outage the last good
// snapshot stays on screen (its own `.degraded` is a stale false) and this flag
// carries the "can't reach Jellyfin" signal.
export function NowPlayingView({
  snapshot,
  degraded,
  conn,
}: {
  snapshot: Snapshot | null;
  degraded: boolean;
  conn: Conn;
}) {
  const summary = snapshot?.summary ?? null;
  const cap = summary && summary.capacity != null && summary.capacity > 0 ? summary.capacity : null;

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">
          Now Playing
        </h1>
        <div className="flex items-center gap-2 font-mono text-[11.5px] text-muted">
          <span>
            {snapshot ? `${snapshot.server.name || "jellyfin"} · ${snapshot.server.version}` : "—"}
          </span>
          <span className="text-line">·</span>
          <ConnDot conn={conn} />
        </div>
      </div>

      {summary && (
        <p className="font-mono text-[12.5px] leading-relaxed text-muted">
          <span className="text-ink">{summary.streams}</span> streams
          {"  ·  "}
          <span className={cn(summary.transcodes > 0 && "text-violet")}>
            {summary.transcodes} transcoding
          </span>
          {"  ·  "}
          {fmtBitrate(summary.outbound_bitrate)} out
          {cap != null && (
            <>
              {"  ·  "}
              <span className="inline-flex items-center gap-1.5 align-middle">
                <span className="inline-block h-1 w-16 overflow-hidden rounded-full bg-line/60">
                  <span
                    className="block h-full rounded-full bg-cyan"
                    style={{ width: `${Math.min(100, (summary.streams / cap) * 100)}%` }}
                  />
                </span>
                <span className="tabular-nums">
                  {summary.streams} / {cap}
                </span>
              </span>
            </>
          )}
        </p>
      )}

      {degraded && (
        <div className="flex items-center gap-2.5 rounded-[14px] border border-mote/40 bg-panel/60 px-4 py-3 text-[13px] text-muted backdrop-blur-xl">
          <span aria-hidden className="h-2 w-2 shrink-0 rounded-full bg-mote" />
          <span>
            {snapshot && snapshot.sessions.length > 0
              ? "Can't reach Jellyfin — showing the last known state."
              : "Can't reach Jellyfin — nothing to show."}
          </span>
        </div>
      )}

      {!snapshot ? (
        <div className="flex flex-col gap-3">
          <Skeleton className="h-[188px] w-full" />
          <Skeleton className="h-[188px] w-full" />
        </div>
      ) : snapshot.sessions.length === 0 && !degraded ? (
        <Panel className="flex flex-col items-center gap-2 px-6 py-16 text-center">
          <span className="relative mb-1 flex h-3 w-3">
            <span className="absolute inline-flex h-full w-full rounded-full bg-cyan/40 motion-safe:animate-ping" />
            <span className="relative inline-flex h-3 w-3 rounded-full bg-cyan/60" />
          </span>
          <p className="text-[14px] text-muted">Nothing playing right now.</p>
          <p className="text-[12px] text-muted/70">
            Ephyra only polls Jellyfin while this page is open.
          </p>
        </Panel>
      ) : (
        <div className={cn("flex flex-col gap-3", degraded && "opacity-60 saturate-50")}>
          {snapshot.sessions.map((s) => (
            <NowPlayingCard key={s.session_id} s={s} />
          ))}
        </div>
      )}
    </div>
  );
}

function NowPlayingPage() {
  const { snapshot, degraded, conn } = useNowPlaying();
  return <NowPlayingView snapshot={snapshot} degraded={degraded} conn={conn} />;
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/now",
  component: NowPlayingPage,
});
