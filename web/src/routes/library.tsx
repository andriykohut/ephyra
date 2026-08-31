import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createRoute } from "@tanstack/react-router";
import { lazy, type ReactNode, Suspense } from "react";
import { libraryOverviewQuery } from "@/api/queries";
import type { DiskBucket, LabeledCount } from "@/api/types";
import { GenreBand } from "@/components/GenreBand";
import { Panel } from "@/components/Panel";
import { Skeleton } from "@/components/Skeleton";
import { StaleBanner } from "@/components/StaleBanner";
import { StatCard } from "@/components/StatCard";
import { fmtBytes, fmtInt, fmtRuntime, timeAgo } from "@/lib/format";
import { Route as rootRoute } from "./__root";

// ECharts is heavy (~200 KB gz). The hero — genre band + stat cards — paints
// first; charts stream in after.
const EChart = lazy(() => import("@/charts/EChart").then((m) => ({ default: m.EChart })));

const GB = 1024 ** 3;

function barOption(rows: { name: string; value: number }[], unit: "GB" | "n") {
  return {
    tooltip: {
      trigger: "axis" as const,
      axisPointer: { type: "shadow" as const },
      valueFormatter: (v: number) => (unit === "GB" ? `${fmtInt(v)} GB` : fmtInt(v)),
    },
    xAxis: { type: "category" as const, data: rows.map((r) => r.name) },
    yAxis: { type: "value" as const },
    series: [
      {
        type: "bar" as const,
        data: rows.map((r) => r.value),
        barMaxWidth: 34,
        itemStyle: { borderRadius: [3, 3, 0, 0] as [number, number, number, number] },
      },
    ],
  };
}

function growthOption(g: { month: string; added_items: number; cum_items: number }[]) {
  return {
    tooltip: { trigger: "axis" as const },
    legend: { data: ["added", "cumulative"], top: 0, left: 0 },
    grid: { left: 4, right: 48, top: 34, bottom: 24, containLabel: true },
    xAxis: {
      type: "category" as const,
      data: g.map((p) => p.month),
      axisLabel: { interval: Math.ceil(g.length / 8) },
    },
    yAxis: [{ type: "value" as const }, { type: "value" as const, splitLine: { show: false } }],
    series: [
      {
        name: "added",
        type: "bar" as const,
        data: g.map((p) => p.added_items),
        barMaxWidth: 18,
        itemStyle: {
          color: "rgba(155,123,255,0.35)",
          borderRadius: [2, 2, 0, 0] as [number, number, number, number],
        },
      },
      {
        name: "cumulative",
        type: "line" as const,
        yAxisIndex: 1,
        smooth: true,
        showSymbol: false,
        data: g.map((p) => p.cum_items),
        lineStyle: { width: 2 },
        areaStyle: { color: "rgba(79,224,216,0.12)" },
      },
    ],
  };
}

export function growthForChart<T extends { month: string }>(g: T[]): T[] {
  // The API returns items with a broken (unix-epoch) DateCreated in a "1970-01"
  // bucket. That's real source data — kept in the API — but plotting it wrecks
  // the axis. Rendering is where this belongs.
  return g.filter((p) => p.month !== "1970-01");
}

const gbRows = (d: DiskBucket[]) =>
  d.map((x) => ({ name: x.bucket, value: Math.round(x.bytes / GB) }));
const nRows = (d: LabeledCount[]) => d.map((x) => ({ name: x.label, value: x.count }));

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

function LibrarySkeleton() {
  return (
    <div className="flex flex-col gap-7">
      <Skeleton className="h-9 w-40" />
      <Skeleton className="h-[104px] w-full" />
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        {["a", "b", "c", "d", "e", "f"].map((k) => (
          <Skeleton key={k} className="h-[74px]" />
        ))}
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        {["p", "q", "r", "s"].map((k) => (
          <Skeleton key={k} className="h-[240px]" />
        ))}
        <Skeleton className="h-[280px] md:col-span-2" />
      </div>
    </div>
  );
}

export function LibraryOverview() {
  const qc = useQueryClient();
  const q = useQuery(libraryOverviewQuery());

  if (q.isPending) return <LibrarySkeleton />;

  if (q.isError) {
    return (
      <Panel className="p-5">
        <div className="font-display text-lg font-semibold text-ink">
          Couldn't load library stats
        </div>
        <p className="mt-1 text-[13px] text-muted">{(q.error as Error).message}</p>
        <button
          type="button"
          onClick={() => q.refetch()}
          className="mt-4 rounded-[10px] border border-line px-3 py-1.5 text-[13px] text-ink hover:border-cyan hover:text-cyan"
        >
          Try again
        </button>
      </Panel>
    );
  }

  const { data, meta } = q.data;
  const t = data.totals;

  const refresh = () => {
    void fetch("/api/refresh?job=library", { method: "POST" }).then(() =>
      setTimeout(() => qc.invalidateQueries({ queryKey: ["library-overview"] }), 1500),
    );
  };

  return (
    <div className="flex flex-col gap-7">
      <div className="flex items-baseline justify-between gap-4">
        <h1 className="font-display text-[clamp(26px,4.5vw,36px)] font-bold tracking-[-0.03em] text-ink">
          Library
        </h1>
        <span className="font-mono text-[11.5px] text-muted">
          updated {timeAgo(meta.generated_at)}
        </span>
      </div>

      {meta.stale && <StaleBanner onRefresh={refresh} />}

      <GenreBand genres={data.genres_top} />

      <p className="font-mono text-[13px] leading-relaxed text-muted">
        <span className="font-medium text-ink">{fmtInt(t.items)}</span> items{"  ·  "}
        <span className="font-medium text-ink">{fmtInt(t.series)}</span> series{"  ·  "}
        <span className="font-medium text-ink">{fmtRuntime(t.runtime_seconds)}</span> runtime
        {"  ·  "}
        <span className="font-medium text-ink">{fmtBytes(t.bytes)}</span> on disk
      </p>

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <StatCard label="items" value={fmtInt(t.items)} />
        <StatCard label="series" value={fmtInt(t.series)} />
        <StatCard label="runtime" value={fmtRuntime(t.runtime_seconds)} />
        <StatCard label="on disk" value={fmtBytes(t.bytes)} />
        <StatCard label="4K · HDR" value={`${fmtInt(t.count_uhd)} · ${fmtInt(t.count_hdr)}`} mote />
        <StatCard label="Dolby Vision" value={fmtInt(t.count_dv)} mote />
      </div>

      <Suspense fallback={<ChartsFallback />}>
        <div className="grid gap-3 md:grid-cols-2">
          <ChartPanel title="disk by resolution">
            <EChart
              ariaLabel="Disk usage by resolution"
              option={barOption(gbRows(data.disk_by_resolution), "GB")}
            />
          </ChartPanel>
          <ChartPanel title="disk by codec">
            <EChart
              ariaLabel="Disk usage by codec"
              option={barOption(gbRows(data.disk_by_codec), "GB")}
            />
          </ChartPanel>
          <ChartPanel title="titles by decade">
            <EChart ariaLabel="Titles by decade" option={barOption(nRows(data.by_decade), "n")} />
          </ChartPanel>
          <ChartPanel title="disk by library">
            <EChart
              ariaLabel="Disk usage by library"
              option={barOption(gbRows(data.disk_by_library), "GB")}
            />
          </ChartPanel>
          <div className="md:col-span-2">
            <ChartPanel title="added over time">
              <EChart
                height={280}
                ariaLabel="Items added over time"
                option={growthOption(growthForChart(data.growth))}
              />
            </ChartPanel>
          </div>
        </div>
      </Suspense>
    </div>
  );
}

function ChartsFallback() {
  return (
    <div className="grid gap-3 md:grid-cols-2">
      {["a", "b", "c", "d"].map((k) => (
        <Skeleton key={k} className="h-[240px]" />
      ))}
      <Skeleton className="h-[280px] md:col-span-2" />
    </div>
  );
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/library",
  component: LibraryOverview,
});
