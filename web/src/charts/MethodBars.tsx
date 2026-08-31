import { lazy, Suspense } from "react";
import type { WatchMethodWeek } from "@/api/types";
import { Skeleton } from "@/components/Skeleton";
import { methodColors } from "./theme";

const EChart = lazy(() => import("./EChart").then((m) => ({ default: m.EChart })));
const ORDER = ["DirectPlay", "Remux", "AudioTranscode", "VideoTranscode", "Other"] as const;

export function MethodBars({ weeks }: { weeks: WatchMethodWeek[] }) {
  const option = {
    tooltip: { trigger: "axis" as const, axisPointer: { type: "shadow" as const } },
    // Five entries wrap to two rows in a half-width panel; reserve room so the
    // second row doesn't sit on top of the bars.
    legend: {
      top: 0,
      left: 0,
      textStyle: { fontSize: 10 },
      itemWidth: 12,
      itemHeight: 8,
      itemGap: 10,
    },
    grid: { left: 4, right: 8, top: 48, bottom: 20, containLabel: true },
    xAxis: { type: "category" as const, data: weeks.map((w) => w.week) },
    yAxis: { type: "value" as const },
    series: ORDER.map((k) => ({
      name: k,
      type: "bar" as const,
      stack: "m",
      data: weeks.map((w) => w[k]),
      itemStyle: { color: methodColors[k] },
      barMaxWidth: 26,
    })),
  };
  return (
    <Suspense fallback={<Skeleton className="h-[240px]" />}>
      <EChart ariaLabel="Play method by week" option={option} />
    </Suspense>
  );
}
