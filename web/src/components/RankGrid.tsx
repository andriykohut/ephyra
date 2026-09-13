import { fmtDuration, fmtInt } from "@/lib/format";
import { cn } from "@/lib/utils";
import { Art } from "./Art";

type RankMetric = "watch_sec" | "distinct_titles";

// The one row shape every ranking speaks: people rows use `person`/`kind`
// natively, and profile.overview.tsx maps items and genres onto the same
// fields so this component never needs to know which source it came from.
export interface RankRow {
  person: string;
  kind: string;
  jf_url: string;
  item_id?: string;
  watch_sec?: number;
  plays?: number;
  distinct_titles?: number;
  watch_sec_played?: number;
  plays_played?: number;
  distinct_titles_played?: number;
}

// Rows with a face or poster worth showing; series and genre rows start at
// the name per the direction spec.
const CHIP_KINDS = new Set(["actor", "director", "movie", "episode"]);

function metricValue(r: RankRow, metric: RankMetric, playedOnly: boolean): number {
  if (metric === "watch_sec") return (playedOnly ? r.watch_sec_played : r.watch_sec) ?? 0;
  return (playedOnly ? r.distinct_titles_played : r.distinct_titles) ?? 0;
}

function fmtValue(metric: RankMetric, v: number): string {
  return metric === "watch_sec" ? fmtDuration(v) : fmtInt(v);
}

function chipSrc(r: RankRow): string | undefined {
  if (r.kind === "actor" || r.kind === "director") {
    return `/api/art/person?name=${encodeURIComponent(r.person)}`;
  }
  if (r.kind === "movie" || r.kind === "episode") {
    return r.item_id ? `/api/art/item/${r.item_id}` : undefined;
  }
  return undefined;
}

export function RankGrid({
  rows,
  metric,
  playedOnly,
  color = "cyan",
  limit = 8,
  empty = "Nothing tracked yet.",
}: {
  rows: RankRow[];
  metric: RankMetric;
  playedOnly: boolean;
  color?: "cyan" | "violet";
  limit?: number;
  empty?: string;
}) {
  const ranked = rows
    .map((row) => ({ row, value: metricValue(row, metric, playedOnly) }))
    .sort((a, b) => b.value - a.value)
    .slice(0, limit);

  // playedOnly defaults on, so an all-zero *_played set (nothing ticked
  // played yet) is as empty as no rows at all -- otherwise every panel shows
  // names with "0s" bars instead of the empty state.
  if (ranked.length === 0 || ranked.every((r) => r.value === 0)) {
    return <p className="px-1 py-4 text-[12px] text-muted">{empty}</p>;
  }

  const max = ranked[0].value || 1;
  const barColor = color === "violet" ? "bg-violet" : "bg-cyan";

  return (
    <div className="flex flex-col gap-2.5">
      {ranked.map(({ row, value }, i) => {
        const showChip = CHIP_KINDS.has(row.kind);
        const pct = Math.max(4, Math.round((value / max) * 100));
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: person/kind alone can repeat across ranges; rank position is stable within one render
          <div key={`${row.kind}-${row.person}-${i}`} className="flex flex-col gap-1">
            <div className="flex items-center gap-2">
              <span className="w-4 shrink-0 text-right font-mono text-[11px] text-muted">
                {i + 1}
              </span>
              {showChip && (
                <Art
                  src={chipSrc(row)}
                  kind="chip"
                  fallback={row.person}
                  className="h-[28px] w-[28px] shrink-0 text-[11px]"
                />
              )}
              {row.jf_url ? (
                <a
                  href={row.jf_url}
                  target="_blank"
                  rel="noreferrer"
                  className="min-w-0 flex-1 truncate border-b border-cyan text-[13px] text-ink"
                >
                  {row.person}
                </a>
              ) : (
                <span className="min-w-0 flex-1 truncate text-[13px] text-ink">{row.person}</span>
              )}
              <span className="shrink-0 font-mono text-[11.5px] tabular-nums text-muted">
                {fmtValue(metric, value)}
              </span>
            </div>
            <div className={cn("h-[3px] rounded bg-line/40", showChip ? "ml-[60px]" : "ml-[24px]")}>
              <div className={cn("h-full rounded", barColor)} style={{ width: `${pct}%` }} />
            </div>
          </div>
        );
      })}
    </div>
  );
}
