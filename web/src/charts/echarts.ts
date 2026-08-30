import { BarChart, LineChart } from "echarts/charts";
import { GridComponent, LegendComponent, TooltipComponent } from "echarts/components";
import * as echarts from "echarts/core";
import { CanvasRenderer } from "echarts/renderers";
import { abyssalTheme } from "./theme";

echarts.use([
  BarChart,
  LineChart,
  GridComponent,
  TooltipComponent,
  LegendComponent,
  CanvasRenderer,
]);

let themed = false;
export function ensureTheme() {
  if (!themed) {
    echarts.registerTheme("abyssal", abyssalTheme());
    themed = true;
  }
}

export { echarts };
