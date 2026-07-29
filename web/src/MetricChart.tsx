import { useEffect, useRef } from "react";
import * as echarts from "echarts";
import type { MetricSeriesSummary } from "./events";
import type { MetricPoint } from "./types";
import { metricSeriesColor } from "./metricColors";

/* Canvas charts cannot resolve CSS variables, so the chrome colours below
   mirror the console theme tokens from styles.css (muted/faint/border). */
const CHART_AXIS_INK = "#7d858c";
const CHART_AXIS_LINE = "#d7dde2";
const CHART_GRID_LINE = "#edf1f4";
const CHART_TOOLTIP_INK = "#24231f";
const CHART_POINTER = "#a3a097";

const X_AXIS_NAMES: Record<"epoch" | "step" | "sample", string> = { epoch: "epoch", step: "step", sample: "sample" };

export function MetricChart({ points, series, compact = false, axisKind = "sample" }: { points: MetricPoint[]; series?: MetricSeriesSummary[]; compact?: boolean; axisKind?: "epoch" | "step" | "sample" }) {
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!ref.current) return;
    const reducedMotion = typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const chart = echarts.init(ref.current);
    const chartSeries = (series?.length ? series : summarizeChartSeries(points, axisKind)).slice(0, 12);
    const multiSeries = chartSeries.length > 1;
    chart.setOption({
      animationDuration: reducedMotion ? 0 : 180,
      color: chartSeries.map((_, index) => metricSeriesColor(index)),
      grid: compact
        ? { left: 46, right: 12, top: multiSeries ? 30 : 12, bottom: 30 }
        : { left: 46, right: 16, top: multiSeries ? 32 : 18, bottom: 32 },
      legend: multiSeries ? {
        type: "scroll",
        top: 0,
        left: 0,
        icon: "roundRect",
        itemWidth: 10,
        itemHeight: 3,
        itemGap: 10,
        textStyle: { color: "#4b4841", fontSize: 10.5 },
        inactiveColor: "#c8c6be",
        pageIconColor: CHART_AXIS_INK,
        pageTextStyle: { color: CHART_AXIS_INK, fontSize: 10 },
        tooltip: { show: true }
      } : undefined,
      tooltip: {
        trigger: "axis",
        confine: true,
        backgroundColor: "rgba(255, 255, 255, 0.96)",
        borderColor: CHART_AXIS_LINE,
        borderWidth: 1,
        padding: [6, 10],
        textStyle: { color: CHART_TOOLTIP_INK, fontSize: 11 },
        axisPointer: { type: "line", lineStyle: { color: CHART_POINTER, type: "dashed", width: 1 } },
        valueFormatter: (value: unknown) => formatMetric(Number(value))
      },
      xAxis: {
        type: "value",
        name: X_AXIS_NAMES[axisKind],
        nameLocation: "middle",
        nameGap: 16,
        nameTextStyle: { color: CHART_AXIS_INK, fontSize: 10 },
        axisLabel: { color: CHART_AXIS_INK, fontSize: 10 },
        axisLine: { lineStyle: { color: CHART_AXIS_LINE } },
        axisTick: { lineStyle: { color: CHART_AXIS_LINE } },
        splitLine: { lineStyle: { color: CHART_GRID_LINE } }
      },
      yAxis: { type: "value", scale: true, axisLabel: { color: CHART_AXIS_INK, fontSize: 10, formatter: (value: number) => formatMetric(value) }, axisLine: { show: true, lineStyle: { color: CHART_AXIS_LINE } }, axisTick: { show: true, lineStyle: { color: CHART_AXIS_LINE } }, splitLine: { lineStyle: { color: CHART_GRID_LINE } } },
      series: chartSeries.map((row, index) => ({
        name: row.label, type: "line", smooth: false, lineStyle: { width: compact ? 1.7 : 2 }, showSymbol: row.points.length <= 1, symbolSize: row.points.length <= 1 ? 8 : 4,
        data: row.points.map((point, idx) => ({ name: row.fullLabel, value: [metricChartAxis(point, idx, axisKind), point.value], itemStyle: { color: metricSeriesColor(index) } }))
      }))
    });
    const resize = () => chart.resize();
    const observer = typeof ResizeObserver !== "undefined" ? new ResizeObserver(resize) : null;
    observer?.observe(ref.current);
    window.addEventListener("resize", resize);
    return () => { observer?.disconnect(); window.removeEventListener("resize", resize); chart.dispose(); };
  }, [points, series, compact, axisKind]);
  return <div className={compact ? "chart chart-compact" : "chart"} ref={ref} />;
}

function metricChartAxis(point: MetricPoint, fallback: number, axisKind: "epoch" | "step" | "sample") {
  if (axisKind === "epoch" && point.epoch != null && Number.isFinite(Number(point.epoch))) return Number(point.epoch);
  if (axisKind === "step" && point.step != null && Number.isFinite(Number(point.step))) return Number(point.step);
  return fallback;
}

function summarizeChartSeries(points: MetricPoint[], axisKind: "epoch" | "step" | "sample"): MetricSeriesSummary[] {
  const grouped = new Map<string, MetricPoint[]>();
  for (const point of points) {
    const key = point.series ? `${point.series}/${point.name}` : point.name;
    grouped.set(key, [...(grouped.get(key) || []), point]);
  }
  return Array.from(grouped.entries()).map(([key, rows]) => ({
    key, label: shortSeriesName(key), fullLabel: key, latest: rows[rows.length - 1], count: rows.length, role: "curve",
    trend: rows.map((row, index) => ({ axis: metricChartAxis(row, index, axisKind), value: row.value })), points: rows
  }));
}

function shortSeriesName(name: string) {
  const cleaned = name.replace(/_/g, " ");
  return cleaned.length <= 28 ? cleaned : `${cleaned.slice(0, 25)}...`;
}

function formatMetric(value: number) {
  if (!Number.isFinite(value)) return "-";
  if (Math.abs(value) >= 1000 || Math.abs(value) < 0.001 && value !== 0) return value.toExponential(3);
  return value.toFixed(4).replace(/0+$/, "").replace(/\.$/, "");
}
