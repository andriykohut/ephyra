import { lazy, Suspense } from "react";
import { Skeleton } from "@/components/Skeleton";
import { heatRamp } from "./theme";

const EChart = lazy(() => import("./EChart").then((m) => ({ default: m.EChart })));
const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export function Heatmap({
  cells,
  ariaLabel,
}: {
  cells: { dow: number; hour: number; watch_sec: number }[];
  ariaLabel: string;
}) {
  const data = cells.map((c) => [c.hour, c.dow, Math.round(c.watch_sec / 3600)]);
  const max = Math.max(1, ...data.map((d) => d[2]));
  const option = {
    tooltip: {
      formatter: (p: { value: [number, number, number] }) =>
        `${DOW[p.value[1]]} ${String(p.value[0]).padStart(2, "0")}:00 · ${p.value[2]} h`,
    },
    grid: { left: 4, right: 8, top: 8, bottom: 24, containLabel: true },
    xAxis: {
      type: "category" as const,
      data: Array.from({ length: 24 }, (_, i) => i),
      splitArea: { show: true },
    },
    yAxis: { type: "category" as const, data: DOW, splitArea: { show: true } },
    visualMap: { min: 0, max, show: false, inRange: { color: heatRamp } },
    series: [
      {
        type: "heatmap" as const,
        data,
        progressive: 0,
        itemStyle: { borderColor: "transparent" },
      },
    ],
  };
  return (
    <Suspense fallback={<Skeleton className="h-[240px]" />}>
      <EChart ariaLabel={ariaLabel} option={option} />
    </Suspense>
  );
}
