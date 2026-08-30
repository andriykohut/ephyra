import { useQuery } from "@tanstack/react-query";
import { createRoute } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { watchStatsQuery } from "@/api/queries";
import type { WatchRange, WatchStats as WS } from "@/api/types";
import { Heatmap } from "@/charts/Heatmap";
import { MethodBars } from "@/charts/MethodBars";
import { type Column, DataTable } from "@/components/DataTable";
import { Panel } from "@/components/Panel";
import { SegmentedControl } from "@/components/SegmentedControl";
import { Skeleton } from "@/components/Skeleton";
import { StaleBanner } from "@/components/StaleBanner";
import { StatCard } from "@/components/StatCard";
import { UserSelect } from "@/components/UserSelect";
import { fmtInt, fmtRuntime } from "@/lib/format";
import { Route as rootRoute } from "./__root";

const RANGES: { value: WatchRange; label: string }[] = [
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
  { value: "1y", label: "1 year" },
  { value: "all", label: "All" },
];

const validateSearch = (s: Record<string, unknown>): { range: WatchRange; user: string } => ({
  range: (["30d", "90d", "1y", "all"].includes(s.range as string) ? s.range : "30d") as WatchRange,
  user: typeof s.user === "string" && s.user ? s.user : "all",
});

const pct = (x: number) => `${Math.round(x * 100)}%`;
const hours = (sec: number) => `${(sec / 3600).toFixed(1)} h`;

function ChartPanel({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Panel className="p-4">
      <div className="mb-3 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
        {title}
      </div>
      {children}
    </Panel>
  );
}

const titleCols: Column<{ name: string; plays: number; watch_sec: number }>[] = [
  { key: "name", header: "title", cell: (r) => r.name },
  { key: "plays", header: "plays", align: "right", cell: (r) => fmtInt(r.plays) },
  { key: "hrs", header: "watch", align: "right", cell: (r) => hours(r.watch_sec) },
];

export function WatchStatsView({
  range,
  user,
  onRange,
  onUser,
}: {
  range: WatchRange;
  user: string;
  onRange: (v: WatchRange) => void;
  onUser: (v: string) => void;
}) {
  const q = useQuery(watchStatsQuery(range, user));

  const controls = (
    <div className="flex flex-wrap items-center gap-3">
      <SegmentedControl ariaLabel="range" value={range} onChange={onRange} options={RANGES} />
      <UserSelect value={user} users={q.data?.data.users ?? []} onChange={onUser} />
    </div>
  );

  if (q.isPending) {
    return (
      <div className="flex flex-col gap-6">
        <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">
          Watch Stats
        </h1>
        {controls}
        <Skeleton className="h-[420px] w-full" />
      </div>
    );
  }
  if (q.isError) {
    return (
      <Panel className="p-5">
        <p className="text-[13px] text-muted">{(q.error as Error).message}</p>
      </Panel>
    );
  }

  const d: WS = q.data.data;
  const hasPlays = d.totals.plays > 0;

  const coreTable = (
    <ChartPanel title="most played · jellyfin counts">
      <DataTable
        columns={[
          { key: "name", header: "title", cell: (r) => r.name },
          { key: "pc", header: "plays", align: "right", cell: (r) => fmtInt(r.play_count) },
        ]}
        rows={d.most_played_core}
        getKey={(r) => r.item_id}
        empty="No play counts recorded yet."
      />
      <p className="mt-2 text-[11px] text-muted">
        From Jellyfin's own play counters, not the plugin — all-time, no watch time.
      </p>
    </ChartPanel>
  );

  return (
    <div className="flex flex-col gap-6">
      <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">
        Watch Stats
      </h1>
      {controls}
      {q.data.meta.stale && <StaleBanner />}

      {!d.plugin_available ? (
        <Panel className="p-5">
          <div className="font-display text-lg font-semibold text-ink">
            Playback Reporting not detected
          </div>
          <p className="mt-1 text-[13px] leading-relaxed text-muted">
            Watch Stats needs the Playback Reporting plugin. In Jellyfin: Dashboard → Plugins →
            Catalog → Playback Reporting → install, then restart Jellyfin. Ephyra picks it up on the
            next refresh.
          </p>
        </Panel>
      ) : !hasPlays ? (
        <Panel className="p-5">
          <div className="font-display text-lg font-semibold text-ink">No plays in this range</div>
          <p className="mt-1 text-[13px] text-muted">
            The plugin has history from {d.coverage.first_play || "—"} to{" "}
            {d.coverage.last_play || "—"} ({fmtInt(d.coverage.total_plays)} plays total).
          </p>
          <button
            type="button"
            onClick={() => onRange("all")}
            className="mt-3 rounded-[10px] border border-line px-3 py-1.5 text-[13px] text-ink hover:border-cyan hover:text-cyan"
          >
            View all
          </button>
        </Panel>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
            <StatCard label="watch time" value={fmtRuntime(d.totals.watch_seconds)} />
            <StatCard label="plays" value={fmtInt(d.totals.plays)} />
            <StatCard label="active users" value={fmtInt(d.totals.active_users)} />
            <StatCard label="direct play" value={pct(d.totals.direct_play_pct)} />
            <StatCard label="video transcode" value={pct(d.totals.video_transcode_pct)} mote />
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            <ChartPanel title="when we watch">
              <Heatmap cells={d.heatmap} ariaLabel="Watch time by day and hour" />
            </ChartPanel>
            <ChartPanel title="play method by week">
              <MethodBars weeks={d.play_method_weekly} />
            </ChartPanel>
          </div>

          <div className="grid gap-3 md:grid-cols-3">
            <ChartPanel title="top series">
              <DataTable
                columns={titleCols}
                rows={d.top_series}
                getKey={(r) => r.series_id}
                empty="—"
              />
            </ChartPanel>
            <ChartPanel title="top movies">
              <DataTable
                columns={titleCols}
                rows={d.top_movies}
                getKey={(r) => r.item_id}
                empty="—"
              />
            </ChartPanel>
            <ChartPanel title="top episodes">
              <DataTable
                columns={titleCols}
                rows={d.top_episodes}
                getKey={(r) => r.item_id}
                empty="—"
              />
            </ChartPanel>
          </div>

          <ChartPanel title="active users">
            <DataTable
              columns={[
                { key: "name", header: "user", cell: (r) => r.name },
                { key: "hrs", header: "watch", align: "right", cell: (r) => hours(r.watch_sec) },
                { key: "plays", header: "plays", align: "right", cell: (r) => fmtInt(r.plays) },
                {
                  key: "titles",
                  header: "titles",
                  align: "right",
                  cell: (r) => fmtInt(r.distinct_titles),
                },
              ]}
              rows={d.active_users}
              getKey={(r) => r.user_id}
              empty="—"
            />
          </ChartPanel>
        </>
      )}

      {coreTable}
    </div>
  );
}

function WatchStatsPage() {
  const { range, user } = Route.useSearch();
  const nav = Route.useNavigate();
  return (
    <WatchStatsView
      range={range}
      user={user}
      onRange={(v) => nav({ search: (s) => ({ ...s, range: v }) })}
      onUser={(v) => nav({ search: (s) => ({ ...s, user: v }) })}
    />
  );
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/watch",
  validateSearch,
  component: WatchStatsPage,
});
