<script setup>
import {
  ArcElement,
  Chart as ChartJS,
  Tooltip,
} from "chart.js";
import { computed } from "vue";
import { Doughnut } from "vue-chartjs";
import { appState } from "@/state/appState";

ChartJS.register(ArcElement, Tooltip);

const props = defineProps({
  rate: {
    type: Number,
    default: 0,
  },
});

const percentage = computed(() => {
  const rate = Number(props.rate);
  if (!Number.isFinite(rate)) {
    return 0;
  }
  return Math.max(0, Math.min(100, rate * 100));
});

const label = computed(() => {
  const rate = Number(props.rate);
  if (!Number.isFinite(rate)) {
    return "--";
  }
  return `${percentage.value.toFixed(2)}%`;
});

function getSegmentBorderRadius(dataIndex) {
  const radius = 5;

  if (percentage.value <= 0) {
    return dataIndex === 1
      ? {
          outerStart: radius,
          outerEnd: radius,
          innerStart: radius,
          innerEnd: radius,
        }
      : 0;
  }

  if (percentage.value >= 100) {
    return dataIndex === 0
      ? {
          outerStart: radius,
          outerEnd: radius,
          innerStart: radius,
          innerEnd: radius,
        }
      : 0;
  }

  return dataIndex === 0
    ? {
        outerStart: radius,
        outerEnd: 0,
        innerStart: radius,
        innerEnd: 0,
      }
    : {
        outerStart: 0,
        outerEnd: radius,
        innerStart: 0,
        innerEnd: radius,
      };
}

function readThemeColor(name, fallback) {
  if (typeof document === "undefined") {
    return fallback;
  }
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || fallback;
}

const chartTheme = computed(() => {
  // Depend on the state so the canvas is redrawn after a runtime theme change.
  void appState.appearanceTheme;
  void appState.effectiveAppearanceTheme;
  return {
    success: readThemeColor("--color-chart-success", "rgb(74 222 128)"),
    track: readThemeColor("--color-chart-track", "rgb(215 222 232)"),
  };
});

const chartData = computed(() => ({
  labels: ["命中", "未命中"],
  datasets: [
    {
      data: [percentage.value, Math.max(0, 100 - percentage.value)],
      backgroundColor: [chartTheme.value.success, chartTheme.value.track],
      borderWidth: 0,
      hoverBorderWidth: 0,
      selfJoin: false,
      borderRadius: ({ dataIndex }) => getSegmentBorderRadius(dataIndex),
    },
  ],
}));

const chartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  cutout: "78%",
  rotation: -90,
  circumference: 360,
  animation: {
    duration: 450,
  },
  events: [],
  plugins: {
    legend: {
      display: false,
    },
    tooltip: {
      enabled: false,
    },
  },
};
</script>

<template>
  <div class="flex flex-col items-center gap-3">
    <div
      class="relative h-[96px] w-[96px] shrink-0"
      role="img"
      :aria-label="`缓存命中率 ${label}`"
    >
      <Doughnut class="h-full w-full" :data="chartData" :options="chartOptions" />
      <div class="pointer-events-none absolute inset-0 flex items-center justify-center">
        <div
          class="text-[20px] leading-none text-[var(--color-text)]"
          style="font-family: var(--font-num)"
        >
          {{ label }}
        </div>
      </div>
    </div>
  </div>
</template>
