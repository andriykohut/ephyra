import { useMemo } from "react";
import type { LabeledCount } from "@/api/types";
import { fmtInt } from "@/lib/format";

// cyan -> violet, brightest first
const RAMP = [
  "#8ff0ea",
  "#4fe0d8",
  "#54c8d6",
  "#7ba7e6",
  "#9b7bff",
  "#8a72d4",
  "#6a5fae",
  "#4c4a86",
];

export function GenreBand({ genres }: { genres: LabeledCount[] }) {
  const rows = useMemo(() => genres.slice(0, 8), [genres]);
  if (rows.length === 0) {
    return (
      <p className="font-mono text-[12px] text-muted">
        No genre data — items may be missing metadata.
      </p>
    );
  }

  return (
    <section aria-label="Library by genre">
      <div className="mb-2 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
        by genre
      </div>
      <div className="flex h-[84px] overflow-hidden rounded-[14px] border border-line/70 shadow-[0_0_46px_-12px_rgba(79,224,216,0.3),inset_0_0_34px_rgba(0,0,0,0.45)]">
        {rows.map((g, i) => (
          <div
            key={g.label}
            title={`${g.label} · ${fmtInt(g.count)}`}
            className="seg-wipe relative min-w-[3px] border-l border-black/25 first:border-l-0"
            style={{
              flex: `${g.count} 0 0`,
              background: RAMP[i % RAMP.length],
              animationDelay: `${i * 70}ms`,
            }}
          >
            <span className="pointer-events-none absolute inset-x-0 top-0 h-px bg-white/15" />
          </div>
        ))}
      </div>
      <div className="mt-2 flex flex-wrap gap-x-5 gap-y-1 font-mono text-[11.5px] text-muted">
        {rows.map((g) => (
          <span key={g.label}>
            <span className="font-medium tabular-nums text-ink">{fmtInt(g.count)}</span>{" "}
            {g.label.toLowerCase()}
          </span>
        ))}
      </div>
    </section>
  );
}
