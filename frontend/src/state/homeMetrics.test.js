import assert from "node:assert/strict";
import test from "node:test";

import {
  activityLevel,
  buildActivityCells,
  buildTrendSeries,
  cacheHitRateFromBucket,
  createEmptyHomeMetricsReport,
  HOME_METRICS_ACTIVITY_WEEKS,
  isActivityEmpty,
  isTrendEmpty,
  normalizeHomeMetricsReport,
} from "./homeMetrics.js";

test("normalizeHomeMetricsReport keeps hourly buckets and does not invent them from daily", () => {
  const report = normalizeHomeMetricsReport({
    range: "24h",
    granularity: "hour",
    timezone: "UTC",
    start: "2026-08-25T16:00:00Z",
    end: "2026-08-26T16:00:00Z",
    generatedAt: "2026-08-26T15:04:00Z",
    dataVersion: "3@2026-08-26T15:04:00Z",
    summary: { providerCallsTotal: 7, requestTokensTotal: 37 },
    daily: [{ date: "2026-08-26", providerCalls: 99, requestTokens: 999 }],
    buckets: [
      { start: "2026-08-26T13:00:00Z", providerCalls: 2, inputTokens: 10, requestTokens: 12, promptTokens: 10 },
      { start: "2026-08-26T15:00:00Z", providerCalls: 5, inputTokens: 20, requestTokens: 25, promptTokens: 20 },
    ],
  });

  assert.equal(report.range, "24h");
  assert.equal(report.granularity, "hour");
  assert.equal(report.dataVersion, "3@2026-08-26T15:04:00Z");
  assert.equal(report.buckets.length, 2);
  assert.equal(report.daily[0].providerCalls, 99);
  assert.equal(buildTrendSeries(report).length, 2);
  assert.equal(buildTrendSeries(report)[1].inputTokens, 20);
});

test("trend series uses a 0-100 percent track separate from token counts", () => {
  const series = buildTrendSeries({
    range: "30d",
    granularity: "day",
    buckets: [
      {
        start: "2026-08-24T00:00:00Z",
        inputTokens: 80,
        outputTokens: 20,
        cacheReadTokens: 20,
        cacheWriteTokens: 10,
        requestTokens: 130,
        promptTokens: 110,
      },
    ],
  });

  assert.equal(series.length, 1);
  assert.equal(series[0].inputTokens, 80);
  assert.equal(series[0].outputTokens, 20);
  assert.equal(series[0].cacheReadTokens, 20);
  assert.equal(series[0].cacheWriteTokens, 10);
  assert.equal(series[0].cacheHitPercent, 20);
  assert.ok(series[0].cacheHitPercent <= 100);
  assert.notEqual(series[0].cacheHitPercent, series[0].inputTokens);
  assert.equal(cacheHitRateFromBucket({
    inputTokens: 80,
    cacheReadTokens: 20,
    promptTokens: 110,
  }), 0.2);
});

test("zero-filled hourly buckets are an empty trend, not mock activity", () => {
  const report = normalizeHomeMetricsReport({
    range: "24h",
    granularity: "hour",
    dataVersion: "1",
    buckets: Array.from({ length: 24 }, (_, index) => ({
      start: `2026-08-25T${String(index).padStart(2, "0")}:00:00Z`,
    })),
    daily: [{ date: "2026-08-26", providerCalls: 12 }],
  });

  assert.equal(report.buckets.length, 24);
  assert.equal(isTrendEmpty(report), true);
});

test("activity cells cover 52 UTC weeks and ignore missing days as zero", () => {
  const now = new Date("2026-08-26T15:04:00Z");
  const cells = buildActivityCells([
    { date: "2026-08-26", providerCalls: 4 },
    { date: "2026-08-20", providerCalls: 1 },
  ], now);

  assert.equal(cells[0].weekday, 0);
  assert.ok(cells.length >= (HOME_METRICS_ACTIVITY_WEEKS - 1) * 7 + 1);
  assert.ok(cells.length <= HOME_METRICS_ACTIVITY_WEEKS * 7);
  assert.equal(cells[0].weekday, 0);
  assert.equal(cells.at(-1).date, "2026-08-26");
  assert.equal(cells.find((cell) => cell.date === "2026-08-26")?.value, 4);
  assert.equal(cells.find((cell) => cell.date === "2026-08-25")?.value, 0);
  assert.equal(isActivityEmpty([{ date: "2026-08-26", providerCalls: 0 }]), true);
  assert.equal(activityLevel(0, 4), 0);
  assert.equal(activityLevel(4, 4), 5);
});

test("empty report helper preserves range fallback without fabricating buckets", () => {
  const empty = createEmptyHomeMetricsReport("24h");
  assert.equal(empty.granularity, "hour");
  assert.deepEqual(empty.buckets, []);
  assert.equal(isTrendEmpty(empty), true);
});