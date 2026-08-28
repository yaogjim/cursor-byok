export const HOME_METRICS_RANGE_OPTIONS = Object.freeze([
  { value: "24h", label: "24 小时" },
  { value: "30d", label: "近 30 天" },
  { value: "all", label: "全部" },
]);

export const DEFAULT_HOME_METRICS_RANGE = "24h";
export const HOME_METRICS_ACTIVITY_WEEKS = 52;

function asString(value) {
  if (typeof value === "string") {
    return value.trim();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

function asCount(value) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return Math.max(0, Math.round(value));
  }
  const text = asString(value);
  if (!text || !/^\d+$/.test(text)) {
    return 0;
  }
  return Number(text);
}

function asNullableRate(value) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  return null;
}

export function createEmptyHomeMetrics() {
  return {
    providerCallsTotal: 0,
    turnsTotal: 0,
    validTurnsTotal: 0,
    invalidTurnsTotal: 0,
    requestTokensTotal: 0,
    promptTokensTotal: 0,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    cacheHitRate: null,
  };
}

export function createEmptyHomeMetricsReport(range = DEFAULT_HOME_METRICS_RANGE) {
  const normalizedRange = asString(range) || DEFAULT_HOME_METRICS_RANGE;
  return {
    range: normalizedRange,
    granularity: normalizedRange === "24h" ? "hour" : "day",
    timezone: "UTC",
    start: "",
    end: "",
    generatedAt: "",
    dataVersion: "",
    summary: createEmptyHomeMetrics(),
    daily: [],
    buckets: [],
  };
}

export function normalizeHomeMetrics(source) {
  const raw = source && typeof source === "object" ? source : {};
  return {
    providerCallsTotal: asCount(raw.providerCallsTotal),
    turnsTotal: asCount(raw.turnsTotal),
    validTurnsTotal: asCount(raw.validTurnsTotal),
    invalidTurnsTotal: asCount(raw.invalidTurnsTotal),
    requestTokensTotal: asCount(raw.requestTokensTotal),
    promptTokensTotal: asCount(raw.promptTokensTotal),
    cacheReadTokens: asCount(raw.cacheReadTokens),
    cacheWriteTokens: asCount(raw.cacheWriteTokens),
    cacheHitRate: asNullableRate(raw.cacheHitRate),
  };
}

function normalizeBucket(source) {
  const raw = source && typeof source === "object" ? source : {};
  return {
    start: asString(raw.start),
    providerCalls: asCount(raw.providerCalls),
    turnsTotal: asCount(raw.turnsTotal),
    validTurnsTotal: asCount(raw.validTurnsTotal),
    invalidTurnsTotal: asCount(raw.invalidTurnsTotal),
    inputTokens: asCount(raw.inputTokens),
    outputTokens: asCount(raw.outputTokens),
    requestTokens: asCount(raw.requestTokens),
    promptTokens: asCount(raw.promptTokens),
    cacheReadTokens: asCount(raw.cacheReadTokens),
    cacheWriteTokens: asCount(raw.cacheWriteTokens),
  };
}

function normalizeDaily(source) {
  const raw = source && typeof source === "object" ? source : {};
  return {
    date: asString(raw.date),
    providerCalls: asCount(raw.providerCalls),
    turnsTotal: asCount(raw.turnsTotal),
    validTurnsTotal: asCount(raw.validTurnsTotal),
    invalidTurnsTotal: asCount(raw.invalidTurnsTotal),
    inputTokens: asCount(raw.inputTokens),
    outputTokens: asCount(raw.outputTokens),
    requestTokens: asCount(raw.requestTokens),
    promptTokens: asCount(raw.promptTokens),
    cacheReadTokens: asCount(raw.cacheReadTokens),
    cacheWriteTokens: asCount(raw.cacheWriteTokens),
  };
}

export function normalizeHomeMetricsReport(raw) {
  const data = raw && typeof raw === "object" ? raw : {};
  const range = asString(data.range) || "all";
  const daily = Array.isArray(data.daily)
    ? data.daily.map(normalizeDaily).filter((item) => item.date)
    : [];
  const buckets = Array.isArray(data.buckets)
    ? data.buckets.map(normalizeBucket).filter((item) => item.start)
    : [];
  const granularity = asString(data.granularity) || (range === "24h" ? "hour" : "day");
  return {
    range,
    granularity,
    timezone: asString(data.timezone) || "UTC",
    start: asString(data.start),
    end: asString(data.end),
    generatedAt: asString(data.generatedAt),
    dataVersion: asString(data.dataVersion),
    summary: normalizeHomeMetrics(data.summary),
    daily,
    buckets,
  };
}

export function inputTokensFromBucket(bucket) {
  const input = asCount(bucket?.inputTokens);
  if (input > 0) {
    return input;
  }
  return Math.max(
    0,
    asCount(bucket?.promptTokens) - asCount(bucket?.cacheReadTokens) - asCount(bucket?.cacheWriteTokens),
  );
}

export function outputTokensFromBucket(bucket) {
  const output = asCount(bucket?.outputTokens);
  if (output > 0) {
    return output;
  }
  return Math.max(0, asCount(bucket?.requestTokens) - asCount(bucket?.promptTokens));
}

export function cacheHitRateFromBucket(bucket) {
  const cacheRead = asCount(bucket?.cacheReadTokens);
  const input = inputTokensFromBucket(bucket);
  const denominator = cacheRead + input;
  if (denominator <= 0) {
    return null;
  }
  return cacheRead / denominator;
}

function parseBucketDate(start) {
  const text = asString(start);
  if (!text) {
    return null;
  }
  const parsed = new Date(text);
  if (Number.isNaN(parsed.getTime())) {
    return null;
  }
  return parsed;
}

export function formatBucketLabel(start, granularity) {
  const parsed = parseBucketDate(start);
  if (!parsed) {
    return asString(start);
  }
  if (granularity === "hour") {
    return `${String(parsed.getUTCHours()).padStart(2, "0")}:00`;
  }
  const month = String(parsed.getUTCMonth() + 1).padStart(2, "0");
  const day = String(parsed.getUTCDate()).padStart(2, "0");
  return `${month}-${day}`;
}

export function buildTrendSeries(report) {
  const normalized = normalizeHomeMetricsReport(report);
  return normalized.buckets.map((bucket) => {
    const cacheHitRate = cacheHitRateFromBucket(bucket);
    return {
      start: bucket.start,
      label: formatBucketLabel(bucket.start, normalized.granularity),
      inputTokens: inputTokensFromBucket(bucket),
      outputTokens: outputTokensFromBucket(bucket),
      cacheReadTokens: asCount(bucket.cacheReadTokens),
      cacheWriteTokens: asCount(bucket.cacheWriteTokens),
      providerCalls: asCount(bucket.providerCalls),
      cacheHitRate,
      cacheHitPercent: cacheHitRate == null ? null : Math.max(0, Math.min(100, cacheHitRate * 100)),
    };
  });
}

export function isTrendEmpty(report) {
  const series = buildTrendSeries(report);
  if (series.length === 0) {
    return true;
  }
  return !series.some((point) => (
    point.inputTokens > 0
    || point.outputTokens > 0
    || point.cacheReadTokens > 0
    || point.cacheWriteTokens > 0
    || point.providerCalls > 0
    || point.cacheHitPercent != null
  ));
}

function utcDay(value) {
  const date = value instanceof Date ? value : new Date(value);
  if (Number.isNaN(date.getTime())) {
    return null;
  }
  return new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate()));
}

function formatUTCDate(date) {
  return date.toISOString().slice(0, 10);
}

export function buildActivityCells(daily, now = new Date()) {
  const end = utcDay(now);
  if (!end) {
    return [];
  }
  const values = new Map(
    (Array.isArray(daily) ? daily : [])
      .map((item) => [asString(item?.date), asCount(item?.providerCalls)])
      .filter(([date]) => date),
  );
  const start = new Date(end);
  start.setUTCDate(start.getUTCDate() - ((HOME_METRICS_ACTIVITY_WEEKS - 1) * 7) - end.getUTCDay());
  const cells = [];
  for (const cursor = new Date(start); cursor <= end; cursor.setUTCDate(cursor.getUTCDate() + 1)) {
    const date = formatUTCDate(cursor);
    cells.push({
      date,
      value: values.get(date) || 0,
      weekday: cursor.getUTCDay(),
    });
  }
  return cells;
}

export function isActivityEmpty(daily) {
  if (!Array.isArray(daily) || daily.length === 0) {
    return true;
  }
  return !daily.some((item) => asCount(item?.providerCalls) > 0);
}

export function activityLevel(value, max) {
  const count = asCount(value);
  if (count <= 0 || max <= 0) {
    return 0;
  }
  const ratio = count / max;
  if (ratio <= 0.2) {
    return 1;
  }
  if (ratio <= 0.4) {
    return 2;
  }
  if (ratio <= 0.6) {
    return 3;
  }
  if (ratio <= 0.8) {
    return 4;
  }
  return 5;
}