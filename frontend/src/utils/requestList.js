const NUMERIC_SORT_FIELDS = new Set(["inputTokens", "outputTokens", "durationMs", "ttftMs"]);

const DETAIL_FIELDS = Object.freeze([
  ["错误分类", (row) => row?.errorCategory],
  ["状态码", (row) => positiveNumber(row?.statusCode)],
  ["状态", (row) => row?.status],
  ["请求 ID", (row) => row?.requestId],
  ["请求模型名称", (row) => row?.requestModelName],
  ["原始请求模型", (row) => row?.requestModel],
  ["上游模型", (row) => row?.model],
  ["缓存读取 token", (row) => finiteNumber(row?.cacheReadTokens)],
  ["缓存写入 token", (row) => finiteNumber(row?.cacheWriteTokens)],
]);

function positiveNumber(value) {
  const number = Number(value);
  return Number.isFinite(number) && number > 0 ? number : null;
}

function finiteNumber(value) {
  if (value === null || value === undefined || value === "") {
    return null;
  }
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
}

function pad(value) {
  return String(value).padStart(2, "0");
}

export function formatRequestTime(value, range) {
  const text = value instanceof Date ? value : String(value ?? "").trim();
  if (text === "") {
    return "—";
  }
  const date = text instanceof Date ? text : new Date(text);
  if (Number.isNaN(date.getTime())) {
    return "—";
  }
  const clock = `${pad(date.getHours())}:${pad(date.getMinutes())}`;
  if (range === "today") {
    return `${clock}:${pad(date.getSeconds())}`;
  }
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${clock}`;
}

export function nextRequestSort(current, key) {
  if (!current || current.key !== key) {
    return { key, direction: "asc" };
  }
  if (current.direction === "asc") {
    return { key, direction: "desc" };
  }
  return null;
}

function sortValue(row, key) {
  if (key === "at") {
    if (row?.at === null || row?.at === undefined || row?.at === "") {
      return null;
    }
    const date = new Date(row.at);
    return Number.isNaN(date.getTime()) ? null : date.getTime();
  }
  return NUMERIC_SORT_FIELDS.has(key) ? finiteNumber(row?.[key]) : null;
}

export function sortRequestRows(rows, sort) {
  const source = Array.isArray(rows) ? rows : [];
  if (!sort?.key || !sort.direction) {
    return source;
  }
  const factor = sort.direction === "desc" ? -1 : 1;
  return source
    .map((row, index) => ({ row, index, value: sortValue(row, sort.key) }))
    .sort((left, right) => {
      if (left.value === null && right.value === null) {
        return left.index - right.index;
      }
      if (left.value === null) {
        return 1;
      }
      if (right.value === null) {
        return -1;
      }
      if (left.value === right.value) {
        return left.index - right.index;
      }
      return (left.value < right.value ? -1 : 1) * factor;
    })
    .map((entry) => entry.row);
}

export function requestRowKey(row, index) {
  return String(row?.modelCallId || row?.requestId || `${row?.at ?? ""}|${row?.provider ?? ""}|${row?.model ?? ""}|${index}`);
}

// 请求模型列优先展示客户端显示名；旧记录没有名称时回落为原始编号。
export function requestModelLabel(row) {
  const name = String(row?.requestModelName || "").trim();
  if (name) {
    return name;
  }
  return String(row?.requestModel || "").trim();
}

export function requestDetailFields(row) {
  return DETAIL_FIELDS.map(([label, read]) => {
    const value = read(row);
    return {
      label,
      value: value === null || value === undefined || value === "" ? "—" : String(value),
    };
  });
}
