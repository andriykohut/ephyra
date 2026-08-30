// The ECharts theme reads the Tailwind @theme tokens off :root so the charts
// track the palette. Fallbacks keep it sane during SSR / tests.
function tok(name: string, fallback: string): string {
  if (typeof document === "undefined") return fallback;
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

export function abyssalTheme() {
  const ink = tok("--color-ink", "#e8eef7");
  const muted = tok("--color-muted", "#8a9bb8");
  const line = tok("--color-line", "#232c46");
  const cyan = tok("--color-cyan", "#4fe0d8");
  const violet = tok("--color-violet", "#9b7bff");

  return {
    color: [cyan, violet, "#f4a259", "#57c7a3", "#e2739a", "#6aa9ff"],
    backgroundColor: "transparent",
    textStyle: { fontFamily: "IBM Plex Mono, monospace", color: muted, fontSize: 11 },
    grid: { left: 6, right: 14, top: 18, bottom: 4, containLabel: true },
    categoryAxis: {
      axisLine: { lineStyle: { color: line } },
      axisTick: { show: false },
      axisLabel: { color: muted },
      splitLine: { show: false },
    },
    valueAxis: {
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: muted },
      splitLine: { lineStyle: { color: line, type: "dashed" as const } },
    },
    tooltip: {
      backgroundColor: "rgba(10,14,27,0.94)",
      borderColor: line,
      borderWidth: 1,
      textStyle: { color: ink },
      extraCssText: "backdrop-filter: blur(6px); border-radius: 10px;",
    },
  };
}
