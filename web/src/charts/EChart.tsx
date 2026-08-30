import type { ECharts, EChartsCoreOption } from "echarts/core";
import { useEffect, useRef } from "react";
import { echarts, ensureTheme } from "./echarts";

interface Props {
  option: EChartsCoreOption;
  height?: number;
  ariaLabel?: string;
  className?: string;
}

export function EChart({ option, height = 240, ariaLabel, className }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const inst = useRef<ECharts | null>(null);

  useEffect(() => {
    if (!ref.current) return;
    ensureTheme();
    const chart = echarts.init(ref.current, "abyssal", { renderer: "canvas" });
    inst.current = chart;
    const ro = new ResizeObserver(() => chart.resize());
    ro.observe(ref.current);
    return () => {
      ro.disconnect();
      chart.dispose();
      inst.current = null;
    };
  }, []);

  useEffect(() => {
    inst.current?.setOption(option, true);
  }, [option]);

  return (
    <div
      ref={ref}
      role="img"
      aria-label={ariaLabel}
      className={className}
      style={{ height, width: "100%" }}
    />
  );
}

export default EChart;
