import { type ReactNode, useState } from "react";
import type { NowSession } from "@/api/types";
import { fmtBitrate } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Panel } from "./Panel";

function mmss(sec: number): string {
  const s = Math.max(0, Math.round(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor(s / 60) % 60;
  const ss = String(s % 60).padStart(2, "0");
  // films roll over to h:mm:ss; anything shorter stays m:ss
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

function resLabel(w: number): string {
  if (w >= 3200) return "4K";
  if (w >= 1400) return "1080p";
  if (w >= 1000) return "720p";
  return w > 0 ? "SD" : "";
}

// The art proxy on the Go side holds the API key; the browser only ever sees
// this relative URL. itemId is validated server-side against ^[0-9a-fA-F-]{8,64}$.
const artURL = (kind: "primary" | "backdrop", id: string, tag: string) =>
  `/api/now-playing/art/${id}?kind=${kind}&tag=${encodeURIComponent(tag)}`;

const METHOD_LABEL: Record<string, string> = {
  DirectPlay: "Direct play",
  DirectStream: "Remux",
  Transcode: "Transcode",
};

type Tone = "default" | "mote" | "cyan" | "violet";

function Chip({ children, tone = "default" }: { children: ReactNode; tone?: Tone }) {
  return (
    <span
      className={cn(
        "rounded-full px-2 py-0.5 text-[10.5px] font-medium tracking-wide",
        tone === "mote" && "bg-mote/15 text-mote",
        tone === "cyan" && "bg-cyan/15 text-cyan",
        tone === "violet" && "bg-violet/15 text-violet",
        tone === "default" && "bg-line/60 text-muted",
      )}
    >
      {children}
    </span>
  );
}

// A single live session. Direct play reads quiet and cyan; a transcode pulls in
// the violet accent bar, a violet progress fill and the codec-conversion block,
// so the two are told apart at a glance from across the room.
export function NowPlayingCard({ s }: { s: NowSession }) {
  // Which item's art failed, not a bare boolean: the card is keyed by
  // session_id, so a boolean would pin the placeholder for the rest of the
  // session even after the user moves to an item whose art is fine.
  const [failedItem, setFailedItem] = useState<string | null>(null);
  const posterOK = failedItem !== s.item_id;

  const transcoding = s.play_method === "Transcode";
  const hdr = Boolean(s.source.video.range) && s.source.video.range !== "SDR";
  const pct = Math.min(100, Math.max(0, s.progress_pct));
  const accent = transcoding ? "bg-violet" : "bg-cyan";
  const fill = s.paused ? "bg-muted/50" : transcoding ? "bg-violet" : "bg-cyan";

  return (
    <Panel className="card-rise relative overflow-hidden">
      {s.art.backdrop && (
        <img
          alt=""
          aria-hidden
          src={artURL("backdrop", s.art.backdrop.item_id, s.art.backdrop.tag)}
          className="pointer-events-none absolute inset-0 h-full w-full scale-110 object-cover opacity-[0.16] blur-2xl"
        />
      )}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 bg-gradient-to-r from-panel/92 via-panel/72 to-panel/45"
      />
      <span aria-hidden className={cn("absolute inset-y-0 left-0 w-[3px]", accent)} />

      <div className="relative flex gap-4 p-4 sm:p-5">
        <div className="w-24 shrink-0 sm:w-28">
          {posterOK && s.art.primary_tag ? (
            <img
              alt={`${s.title} poster`}
              src={artURL("primary", s.item_id, s.art.primary_tag)}
              onError={() => setFailedItem(s.item_id)}
              decoding="async"
              className="aspect-[2/3] w-full rounded-lg object-cover shadow-lg shadow-abyss/60 ring-1 ring-line/70"
            />
          ) : (
            <div className="flex aspect-[2/3] w-full items-center justify-center rounded-lg bg-gradient-to-br from-cyan/20 to-violet/20 font-display text-2xl text-ink ring-1 ring-line/70">
              {s.title.slice(0, 1) || "?"}
            </div>
          )}
        </div>

        <div className="flex min-w-0 flex-1 flex-col gap-3">
          <div className="min-w-0">
            <div className="truncate font-display text-[15px] font-semibold text-ink sm:text-base">
              {s.title}
            </div>
            <div className="mt-0.5 truncate text-[12px] text-muted">
              {s.series ? [s.series, s.season_episode].filter(Boolean).join(" · ") : s.type}
              {"  ·  "}
              {s.user}
            </div>
          </div>

          <div>
            <div className="relative">
              <div className="h-1.5 overflow-hidden rounded-full bg-line/50">
                <div
                  className={cn(
                    "h-full rounded-full motion-safe:transition-[width] motion-safe:duration-1000 motion-safe:ease-linear",
                    fill,
                  )}
                  style={{ width: `${pct}%` }}
                />
              </div>
              {!s.paused && pct > 0 && pct < 100 && (
                <span
                  aria-hidden
                  className={cn(
                    "absolute top-1/2 h-2.5 w-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full motion-safe:transition-[left] motion-safe:duration-1000 motion-safe:ease-linear motion-safe:animate-pulse",
                    transcoding
                      ? "bg-violet shadow-[0_0_8px_2px_rgba(155,123,255,0.55)]"
                      : "bg-cyan shadow-[0_0_8px_2px_rgba(79,224,216,0.55)]",
                  )}
                  style={{ left: `${pct}%` }}
                />
              )}
            </div>
            <div className="mt-1 flex items-center justify-between font-mono text-[11px] text-muted">
              <span>
                {mmss(s.position_sec)}
                {"  /  "}
                {mmss(s.runtime_sec)}
              </span>
              <span className="tabular-nums">{Math.round(pct)}%</span>
            </div>
          </div>

          <div className="flex flex-wrap items-center gap-1.5">
            <Chip tone={transcoding ? "violet" : "cyan"}>
              {METHOD_LABEL[s.play_method] ?? s.play_method}
            </Chip>
            <Chip tone={s.is_remote ? "mote" : "default"}>{s.is_remote ? "remote" : "local"}</Chip>
            {s.paused && <Chip tone="mote">paused</Chip>}
            <span aria-hidden className="mx-0.5 h-3 w-px bg-line/70" />
            {resLabel(s.source.video.width) && <Chip>{resLabel(s.source.video.width)}</Chip>}
            {s.source.video.codec && <Chip>{s.source.video.codec.toUpperCase()}</Chip>}
            <Chip tone={hdr ? "mote" : "default"}>{s.source.video.range || "SDR"}</Chip>
            {s.source.audio.codec && (
              <Chip>
                {s.source.audio.codec.toUpperCase()}
                {s.source.audio.layout ? ` ${s.source.audio.layout}` : ""}
              </Chip>
            )}
          </div>

          {s.transcode && (
            <div className="rounded-lg border border-violet/25 bg-abyss/50 p-2.5 font-mono text-[11px]">
              <div className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
                <span className="text-muted">video</span>
                <span className="truncate text-ink">{s.transcode.video || "direct"}</span>
                <span className="text-muted">audio</span>
                <span className="truncate text-ink">{s.transcode.audio || "direct"}</span>
                <span className="text-muted">container</span>
                <span className="truncate text-ink">{s.transcode.container || "direct"}</span>
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-[10.5px] text-muted">
                <span>↑ {fmtBitrate(s.transcode.bitrate)}</span>
                {s.transcode.hw && (
                  <span className="rounded bg-violet/15 px-1.5 py-0.5 text-violet">
                    hw {s.transcode.hw}
                  </span>
                )}
                <span className="ml-auto tabular-nums">
                  buffered {Math.round(s.transcode.completion_pct)}%
                </span>
              </div>
              {s.transcode.reasons.length > 0 && (
                <div className="mt-2 flex flex-wrap gap-1">
                  {s.transcode.reasons.map((r) => (
                    <span
                      key={r}
                      className="rounded bg-line/60 px-1.5 py-0.5 text-[10px] text-muted"
                    >
                      {r}
                    </span>
                  ))}
                </div>
              )}
            </div>
          )}

          <div className="truncate text-[11px] text-muted">
            {s.client}
            {"  ·  "}
            {s.device}
          </div>
        </div>
      </div>
    </Panel>
  );
}
