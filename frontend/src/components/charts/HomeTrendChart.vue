<script setup>
import {
  CategoryScale,
  Chart as ChartJS,
  Filler,
  Legend,
  LinearScale,
  LineElement,
  PointElement,
  Tooltip,
} from "chart.js";
import { computed } from "vue";
import { Line } from "vue-chartjs";
import { appState } from "@/state/appState";
import { buildTrendSeries } from "@/state/homeMetrics";
import { formatCompactInteger } from "@/utils/numberFormat";

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Filler, Tooltip, Legend);

const props = defineProps({
  report: {
    type: Object,
    required: true,
  },
});

const series = computed(() => buildTrendSeries(props.report));

const ariaLabel = computed(() => {
  const points = series.value;
  if (!points.length) {
    return "Token 使用趋势，暂无数据";
  }
  const totalInput = points.reduce((sum, point) => sum + point.inputTokens, 0);
  const totalOutput = points.reduce((sum, point) => sum + point.outputTokens, 0);
  return `Token 使用趋势，Input ${formatCompactInteger(totalInput)}，Output ${formatCompactInteger(totalOutput)}，命中率使用独立百分比轴`;
});

function readThemeColor(name, fallback) {
  if (typeof document === "undefined") {
    return fallback;
  }
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

const chartTheme = computed(() => {
  // Depend on the resolved theme so system-mode OS changes redraw the canvas.
  void appState.appearanceTheme;
  void appState.effectiveAppearanceTheme;
  return {
    text: readThemeColor("--color-text-muted", "#6b7280"),
    grid: readThemeColor("--color-border", "#e8ecef"),
    surface: readThemeColor("--color-surface", "#ffffff"),
  };
});

const chartData = computed(() => ({
  labels: series.value.map((point) => point.label),
  datasets: [
    {
      label: "Input",
      data: series.value.map((point) => point.inputTokens),
      yAxisID: "tokens",
      borderColor: "#3B82F6",
      backgroundColor: "#3B82F6",
      borderWidth: 2,
      pointRadius: 0,
      tension: 0.25,
    },
    {
      label: "Output",
      data: series.value.map((point) => point.outputTokens),
      yAxisID: "tokens",
      borderColor: "#10B981",
      backgroundColor: "#10B981",
      borderWidth: 2,
      pointRadius: 0,
      tension: 0.25,
    },
    {
      label: "Cache 写入",
      data: series.value.map((point) => point.cacheWriteTokens),
      yAxisID: "tokens",
      borderColor: "#F59E0B",
      backgroundColor: "#F59E0B",
      borderWidth: 2,
      pointRadius: 0,
      tension: 0.25,
    },
    {
      label: "Cache 读取",
      data: series.value.map((point) => point.cacheReadTokens),
      yAxisID: "tokens",
      borderColor: "#06B6D4",
      backgroundColor: "#06B6D4",
      borderWidth: 2,
      pointRadius: 0,
      tension: 0.25,
    },
    {
      label: "缓存命中率",
      data: series.value.map((point) => point.cacheHitPercent),
      yAxisID: "rate",
      borderColor: "#8B5CF6",
      backgroundColor: "#8B5CF6",
      borderWidth: 2,
      borderDash: [7, 7],
      pointRadius: 0,
      tension: 0.25,
      spanGaps: true,
    },
  ],
}));

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: {
    mode: "index",
    intersect: false,
  },
  plugins: {
    legend: {
      display: true,
      position: "top",
      align: "start",
      labels: {
        boxWidth: 10,
        boxHeight: 10,
        color: chartTheme.value.text,
        font: { size: 11 },
      },
    },
    tooltip: {
      callbacks: {
        label(context) {
          const label = context.dataset.label || "";
          const value = context.parsed.y;
          if (value == null || !Number.isFinite(value)) {
            return `${label}: 暂无`;
          }
          if (context.dataset.yAxisID === "rate") {
            return `${label}: ${value.toFixed(2)}%`;
          }
          return `${label}: ${formatCompactInteger(value)}`;
        },
      },
    },
  },
  scales: {
    x: {
      ticks: { color: chartTheme.value.text, maxRotation: 0, autoSkip: true, font: { size: 10 } },
      grid: { color: chartTheme.value.grid },
    },
    tokens: {
      type: "linear",
      position: "left",
      beginAtZero: true,
      title: {
        display: true,
        text: "Token",
        color: chartTheme.value.text,
      },
      ticks: {
        color: chartTheme.value.text,
        callback(value) {
          return formatCompactInteger(value);
        },
      },
      grid: { color: chartTheme.value.grid },
    },
    rate: {
      type: "linear",
      position: "right",
      min: 0,
      max: 100,
      title: {
        display: true,
        text: "命中率 %",
        color: chartTheme.value.text,
      },
      ticks: {
        color: chartTheme.value.text,
        callback(value) {
          return `${value}%`;
        },
      },
      grid: { drawOnChartArea: false },
    },
  },
}));
</script>

<template>
  <div
    class="relative h-[240px] w-full"
    role="img"
    :aria-label="ariaLabel"
  >
    <Line class="h-full w-full" :data="chartData" :options="chartOptions" />
  </div>
</template>