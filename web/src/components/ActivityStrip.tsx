import type { ActivityDay } from "@/api/types";

const DOWS = 7;
const STEPS = 4;

function startOfLocalDay(d: Date): Date {
  const r = new Date(d);
  r.setHours(0, 0, 0, 0);
  return r;
}

function addDays(d: Date, n: number): Date {
  const r = new Date(d);
  r.setDate(r.getDate() + n);
  return r;
}

function localISODate(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

function cellColor(sec: number, max: number): string {
  if (sec <= 0 || max <= 0) return "var(--color-well)";
  const level = Math.min(STEPS - 1, Math.ceil((sec / max) * (STEPS - 1)));
  const pct = Math.round((level / (STEPS - 1)) * 100);
  return `color-mix(in oklab, var(--color-cyan) ${pct}%, var(--color-well))`;
}

interface Cell {
  key: string;
  sec: number;
}

// A year-of-days grid: 7 rows of day-of-week, columns of weeks, ending today
// -- the same 365-day span the API bounds `activity` to (ReadProfileOverview).
// Snapping the start back to the preceding Sunday always lands on 53 columns,
// whatever day of the week "today" falls on (the 365-371 day span this
// produces divides into 53 weeks either way).
//
// The API only returns days with a watch_events_daily row, so a quiet
// stretch is otherwise just absent rather than a run of zero cells -- this
// zero-fills every historical slot instead, so a short history still draws a
// full grid rather than a ragged partial one. Slots past today aren't
// zero-filled -- they haven't happened, so they render as blank spacers.
export function ActivityStrip({ days }: { days: ActivityDay[] }) {
  const bySec = new Map(days.map((d) => [d.day, d.watch_sec]));
  const today = startOfLocalDay(new Date());
  const windowStart = addDays(today, -364);
  const gridStart = addDays(windowStart, -windowStart.getDay());
  const cols = Math.floor((today.getTime() - gridStart.getTime()) / (DOWS * 86400000)) + 1;

  const grid: (Cell | null)[][] = Array.from({ length: cols }, (_, col) =>
    Array.from({ length: DOWS }, (_, row) => {
      const date = addDays(gridStart, col * DOWS + row);
      if (date > today) return null;
      const key = localISODate(date);
      return { key, sec: bySec.get(key) ?? 0 };
    }),
  );
  const cells = grid.flat();
  const max = Math.max(0, ...cells.map((c) => c?.sec ?? 0));

  return (
    <div
      role="img"
      aria-label="watch activity, past year"
      className="grid gap-[2px]"
      style={{
        gridAutoFlow: "column",
        gridTemplateRows: `repeat(${DOWS}, 9px)`,
        gridAutoColumns: "9px",
      }}
    >
      {cells.map((c, i) =>
        c ? (
          <span
            key={c.key}
            title={c.key}
            className="h-[9px] w-[9px] rounded-[2px]"
            style={{ background: cellColor(c.sec, max) }}
          />
        ) : (
          // biome-ignore lint/suspicious/noArrayIndexKey: blank future slots carry no identity of their own
          <span key={`blank-${i}`} className="h-[9px] w-[9px]" />
        ),
      )}
    </div>
  );
}
