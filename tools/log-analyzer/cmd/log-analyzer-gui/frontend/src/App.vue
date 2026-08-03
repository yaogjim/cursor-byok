<script setup>
import { computed, onMounted, ref } from "vue";
import { analyzerApi } from "./analyzerApi";

const tabs = [
  ["overview", "概览"],
  ["events", "事件检索"],
  ["findings", "诊断发现"],
  ["appLogs", "App 日志"],
  ["cases", "调查闭环"],
];

const activeTab = ref("overview");
const state = ref({ Opened: false, Summary: {} });
const inputPath = ref("");
const baselinePath = ref("");
const allowUnknown = ref(false);
const busy = ref(false);
const errorMessage = ref("");
const notice = ref("");

const metrics = ref([]);
const metricTotal = ref(0);
const metricCursor = ref("");
const findings = ref([]);
const findingTotal = ref(0);
const findingCursor = ref(0);
const events = ref([]);
const eventTotal = ref(0);
const eventCursor = ref(null);
const eventCursorHistory = ref([]);
const eventQuery = ref("");
const selectedEvent = ref(null);
const payload = ref(null);
const payloadBusy = ref(false);
const appLogs = ref([]);
const appLogTotal = ref(0);
const appLogCursor = ref(0);
const appLogKeyword = ref("");
const appLogSeverity = ref("");
const savedQueries = ref([]);
const savedQueryName = ref("");

const opened = computed(() => Boolean(field(state.value, "Opened", "opened")));
const summary = computed(() => field(state.value, "Summary", "summary") || {});
const eventCount = computed(() => Number(field(summary.value, "EventCount", "event_count") || 0));
const traceCount = computed(() => Number(field(summary.value, "TraceCount", "trace_count") || 0));
const findingCount = computed(() => Number(field(summary.value, "FindingCount", "finding_count") || 0));

function field(value, ...keys) {
  if (!value) return undefined;
  for (const key of keys) {
    if (value[key] !== undefined) return value[key];
  }
  return undefined;
}

function rows(page, ...keys) {
  return field(page, ...keys) || [];
}

function messageOf(error) {
  return error?.message || String(error || "未知错误");
}

async function run(action, success = "") {
  busy.value = true;
  errorMessage.value = "";
  notice.value = "";
  try {
    const result = await action();
    notice.value = success;
    return result;
  } catch (error) {
    errorMessage.value = messageOf(error);
    throw error;
  } finally {
    busy.value = false;
  }
}

async function chooseInput(target) {
  const selected = await analyzerApi.selectInputDirectory();
  if (!selected) return;
  if (target === "baseline") baselinePath.value = selected;
  else inputPath.value = selected;
}

async function openProject() {
  await run(async () => {
    state.value = await analyzerApi.openProject({
      input: inputPath.value,
      baseline: baselinePath.value,
      allow_unknown_schema: allowUnknown.value,
    });
    await Promise.all([loadMetrics(true), loadFindings(true), searchEvents(true), searchAppLogs(true)]);
    activeTab.value = "overview";
  }, "日志项目已打开");
}

async function closeProject() {
  await run(async () => {
    await analyzerApi.closeProject();
    state.value = { Opened: false, Summary: {} };
    metrics.value = [];
    findings.value = [];
    events.value = [];
    appLogs.value = [];
    selectedEvent.value = null;
    payload.value = null;
  }, "临时分析 workspace 已清理");
}

async function loadMetrics(reset = false) {
  if (!opened.value) return;
  if (reset) metricCursor.value = "";
  const page = await analyzerApi.listDiagnosticMetrics(metricCursor.value, 100);
  metrics.value = reset ? rows(page, "Metrics", "metrics") : [...metrics.value, ...rows(page, "Metrics", "metrics")];
  metricTotal.value = Number(field(page, "Total", "total") || 0);
  metricCursor.value = field(page, "NextCursor", "next_cursor") || "";
}

async function loadFindings(reset = false) {
  if (!opened.value) return;
  if (reset) findingCursor.value = 0;
  const page = await analyzerApi.listFindings(findingCursor.value, 100);
  findings.value = reset ? rows(page, "Findings", "findings") : [...findings.value, ...rows(page, "Findings", "findings")];
  findingTotal.value = Number(field(page, "Total", "total") || 0);
  findingCursor.value = Number(field(page, "NextCursor", "next_cursor") || 0);
}

async function searchEvents(reset = false) {
  if (!opened.value) return;
  if (reset) {
    eventCursor.value = null;
    eventCursorHistory.value = [];
  }
  const page = await run(() => analyzerApi.searchEvents({
    query: eventQuery.value,
    after: eventCursor.value,
    limit: 100,
    descending: true,
  }));
  events.value = rows(page, "Events", "events");
  eventTotal.value = Number(field(page, "Total", "total") || 0);
  const next = field(page, "NextCursor", "next_cursor") || null;
  if (next) eventCursorHistory.value.push(eventCursor.value);
  eventCursor.value = next;
}

async function nextEvents() {
  if (!eventCursor.value) return;
  await searchEvents(false);
}

async function previousEvents() {
  if (eventCursorHistory.value.length < 2) {
    await searchEvents(true);
    return;
  }
  eventCursorHistory.value.pop();
  eventCursor.value = eventCursorHistory.value.pop() || null;
  await searchEvents(false);
}

function appendFilter(token) {
  const current = eventQuery.value.trim();
  eventQuery.value = current ? `${current} ${token}` : token;
}

async function selectEvent(event) {
  selectedEvent.value = event;
  payload.value = null;
}

async function readPayload() {
  const order = Number(field(selectedEvent.value, "IngestOrder", "ingest_order") || 0);
  if (!order) return;
  payloadBusy.value = true;
  errorMessage.value = "";
  try {
    payload.value = await analyzerApi.readEventPayload(order);
  } catch (error) {
    errorMessage.value = messageOf(error);
  } finally {
    payloadBusy.value = false;
  }
}

async function searchAppLogs(reset = false) {
  if (!opened.value) return;
  if (reset) appLogCursor.value = 0;
  const page = await run(() => analyzerApi.searchAppLogs({
    keyword: appLogKeyword.value,
    severity: appLogSeverity.value,
    after_id: appLogCursor.value,
    limit: 200,
  }));
  appLogs.value = reset ? rows(page, "Lines", "lines") : [...appLogs.value, ...rows(page, "Lines", "lines")];
  appLogTotal.value = Number(field(page, "Total", "total") || 0);
  appLogCursor.value = Number(field(page, "NextCursor", "next_cursor") || 0);
}

async function exportReport() {
  const output = await analyzerApi.selectExportDirectory();
  if (!output) return;
  await run(() => analyzerApi.exportReport(output), `报告已导出到 ${output}`);
}

async function refreshSavedQueries() {
  savedQueries.value = await analyzerApi.listSavedQueries();
}

async function saveCurrentQuery() {
  if (!savedQueryName.value.trim() || !eventQuery.value.trim()) return;
  await run(async () => {
    await analyzerApi.saveQuery({ Name: savedQueryName.value.trim(), DSL: eventQuery.value.trim() });
    savedQueryName.value = "";
    await refreshSavedQueries();
  }, "查询已保存");
}

async function deleteSavedQuery(id) {
  await run(async () => {
    await analyzerApi.deleteSavedQuery(id);
    await refreshSavedQueries();
  });
}

function useSavedQuery(item) {
  eventQuery.value = field(item, "DSL", "dsl") || "";
  activeTab.value = "events";
  searchEvents(true).catch(() => {});
}

function statusClass(value) {
  const normalized = String(value || "").toLowerCase();
  if (["error", "failed", "timeout", "unsupported"].includes(normalized)) return "danger";
  if (["warning", "partial", "compat", "compat_only", "degraded"].includes(normalized)) return "warning";
  return "neutral";
}

function formatTime(value) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? String(value) : date.toLocaleString();
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

onMounted(async () => {
  busy.value = true;
  try {
    const initialization = await analyzerApi.initialize();
    state.value = field(initialization, "State", "state") || { Opened: false, Summary: {} };
    inputPath.value = field(initialization, "DefaultInput", "default_input") || field(state.value, "Input", "input") || "";
    notice.value = field(initialization, "Warning", "warning") || (opened.value ? "已自动加载客户端默认日志目录" : "");
    await refreshSavedQueries();
    if (opened.value) {
      baselinePath.value = field(state.value, "Baseline", "baseline") || "";
      await Promise.all([loadMetrics(true), loadFindings(true), searchEvents(true), searchAppLogs(true)]);
    }
  } catch (error) {
    errorMessage.value = messageOf(error);
  } finally {
    busy.value = false;
  }
});
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <div>
        <div class="eyebrow">CURSOR BYOK · LOCAL ANALYSIS</div>
        <h1>日志分析器</h1>
      </div>
      <div class="top-actions">
        <button v-if="busy && !opened" class="button ghost" @click="closeProject">取消分析</button>
        <button v-if="opened" class="button ghost" :disabled="busy" @click="exportReport">导出报告</button>
        <button v-if="opened" class="button danger-button" :disabled="busy" @click="closeProject">关闭项目</button>
      </div>
    </header>

    <main>
      <div v-if="errorMessage" class="banner error-banner">{{ errorMessage }}</div>
      <div v-if="notice" class="banner notice-banner">{{ notice }}</div>

      <section v-if="!opened" class="welcome-grid">
        <article class="welcome-card">
          <div class="icon-tile">01</div>
          <h2>打开日志项目</h2>
          <p>输入只读加载，分析期间使用临时 SQLite workspace；关闭应用或项目后自动删除。</p>
          <label>当前日志目录</label>
          <div class="path-row">
            <input v-model="inputPath" placeholder="选择包含 events.jsonl 或日志根目录的位置" />
            <button class="button ghost" @click="chooseInput('input')">浏览</button>
          </div>
          <label>对比基线目录 <span>可选</span></label>
          <div class="path-row">
            <input v-model="baselinePath" placeholder="用于修复前后对比" />
            <button class="button ghost" @click="chooseInput('baseline')">浏览</button>
          </div>
          <label class="checkbox-row"><input v-model="allowUnknown" type="checkbox" />兼容读取未知 schema，并生成警告</label>
          <button class="button primary wide" :disabled="busy || !inputPath.trim()" @click="openProject">
            {{ busy ? "正在分析…" : "打开并分析" }}
          </button>
        </article>
        <aside class="principles-card">
          <div class="eyebrow">PRIVACY BY DEFAULT</div>
          <h3>本地、只读、可审计</h3>
          <ul>
            <li>不修改输入日志</li>
            <li>不调用外部 AI 或网络服务</li>
            <li>payload 仅按事件显式读取</li>
            <li>Prompt 与源码不进入临时索引</li>
          </ul>
        </aside>
      </section>

      <template v-else>
        <section class="project-strip">
          <div><span>当前项目</span><strong>{{ field(state, 'Input', 'input') }}</strong></div>
          <div v-if="field(state, 'HasBaseline', 'has_baseline')"><span>对比模式</span><strong>已加载 baseline</strong></div>
          <button class="refresh-link" :disabled="busy" @click="openProject">手动重新加载</button>
        </section>

        <nav class="tabs">
          <button v-for="tab in tabs" :key="tab[0]" :class="{ active: activeTab === tab[0] }" @click="activeTab = tab[0]">{{ tab[1] }}</button>
        </nav>

        <section v-if="activeTab === 'overview'" class="content-stack">
          <div class="stat-grid">
            <article class="stat-card accent"><span>事件总数</span><strong>{{ eventCount.toLocaleString() }}</strong><small>结构化事件</small></article>
            <article class="stat-card"><span>Trace</span><strong>{{ traceCount.toLocaleString() }}</strong><small>重建链路</small></article>
            <article class="stat-card"><span>Finding</span><strong>{{ findingCount.toLocaleString() }}</strong><small>需人工确认</small></article>
            <article class="stat-card"><span>Baseline</span><strong>{{ field(state, 'HasBaseline', 'has_baseline') ? 'ON' : 'OFF' }}</strong><small>修复前后比较</small></article>
          </div>
          <article class="panel">
            <div class="panel-heading"><div><h2>多维诊断指标</h2><p>项目、能力、操作、路由和目标维度的完成量与延迟。</p></div><span>{{ metrics.length }} / {{ metricTotal }}</span></div>
            <div class="table-wrap">
              <table>
                <thead><tr><th>维度</th><th>值</th><th>事件</th><th>完成</th><th>失败 / 降级</th><th>P50</th><th>P95</th><th>P99</th><th>响应量</th></tr></thead>
                <tbody>
                  <tr v-for="item in metrics" :key="`${field(item, 'Dimension', 'dimension')}:${field(item, 'Value', 'value')}`">
                    <td><span class="pill neutral">{{ field(item, 'Dimension', 'dimension') }}</span></td>
                    <td class="mono truncate">{{ field(item, 'Value', 'value') }}</td>
                    <td>{{ field(item, 'EventCount', 'event_count') }}</td><td>{{ field(item, 'CompletedCount', 'completed_count') }}</td>
                    <td>{{ field(item, 'FailedCount', 'failed_count') }} / {{ field(item, 'DegradedCount', 'degraded_count') }}</td>
                    <td>{{ field(item, 'DurationP50MS', 'duration_p50_ms') }} ms</td><td>{{ field(item, 'DurationP95MS', 'duration_p95_ms') }} ms</td><td>{{ field(item, 'DurationP99MS', 'duration_p99_ms') }} ms</td>
                    <td>{{ formatBytes(field(item, 'ResponseBytes', 'response_bytes')) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <button v-if="metricCursor" class="load-more" @click="loadMetrics(false)">加载更多指标</button>
          </article>
        </section>

        <section v-else-if="activeTab === 'events'" class="content-stack">
          <article class="panel search-panel">
            <div class="panel-heading"><div><h2>事件检索</h2><p>使用白名单 DSL，所有值均以参数传入 SQLite。</p></div><span>{{ eventTotal.toLocaleString() }} 条</span></div>
            <div class="search-row"><input v-model="eventQuery" class="query-input mono" placeholder='例如 severity:error capability:tool outcome:failed' @keyup.enter="searchEvents(true)" /><button class="button primary" :disabled="busy" @click="searchEvents(true)">检索</button></div>
            <div class="quick-filters"><button @click="appendFilter('severity:error')">错误</button><button @click="appendFilter('severity:warning')">警告</button><button @click="appendFilter('capability:tool')">工具</button><button @click="appendFilter('implementation:compat')">Compat</button><button @click="appendFilter('outcome:partial')">Partial</button><button @click="eventQuery = ''">清空</button></div>
            <div class="saved-query-row"><input v-model="savedQueryName" placeholder="保存当前查询的名称" /><button class="button ghost" :disabled="!savedQueryName.trim() || !eventQuery.trim()" @click="saveCurrentQuery">保存查询</button></div>
            <div v-if="savedQueries.length" class="saved-list"><span v-for="item in savedQueries" :key="field(item, 'ID', 'id')"><button @click="useSavedQuery(item)">{{ field(item, 'Name', 'name') }}</button><button class="delete-chip" @click="deleteSavedQuery(field(item, 'ID', 'id'))">×</button></span></div>
          </article>
          <article class="panel">
            <div class="table-wrap event-table">
              <table><thead><tr><th>时间</th><th>级别</th><th>能力 / 操作</th><th>事件</th><th>结果</th><th>Trace</th><th>耗时</th></tr></thead>
                <tbody><tr v-for="item in events" :key="field(item, 'IngestOrder', 'ingest_order')" class="clickable" @click="selectEvent(item)">
                  <td>{{ formatTime(field(item, 'Timestamp', 'timestamp')) }}</td><td><span class="pill" :class="statusClass(field(item, 'Severity', 'severity'))">{{ field(item, 'Severity', 'severity') || 'info' }}</span></td>
                  <td><strong>{{ field(item, 'Capability', 'capability') || '—' }}</strong><small>{{ field(item, 'Operation', 'operation') || '—' }}</small></td><td>{{ field(item, 'Event', 'event') }}</td>
                  <td><span class="pill" :class="statusClass(field(item, 'SemanticOutcome', 'semantic_outcome'))">{{ field(item, 'SemanticOutcome', 'semantic_outcome') || field(item, 'Status', 'status') || '—' }}</span></td>
                  <td class="mono truncate">{{ field(item, 'TraceID', 'trace_id') || field(item, 'TraceKey', 'trace_key') }}</td><td>{{ field(item, 'DurationMS', 'duration_ms') || 0 }} ms</td>
                </tr></tbody>
              </table>
            </div>
            <div class="pager"><button class="button ghost" :disabled="eventCursorHistory.length === 0" @click="previousEvents">上一页</button><button class="button ghost" :disabled="!eventCursor" @click="nextEvents">下一页</button></div>
          </article>
        </section>

        <section v-else-if="activeTab === 'findings'" class="content-stack">
          <article class="panel"><div class="panel-heading"><div><h2>诊断发现</h2><p>规则结论是调查入口，不替代人工根因确认。</p></div><span>{{ findings.length }} / {{ findingTotal }}</span></div>
            <div class="finding-grid"><article v-for="item in findings" :key="field(item, 'ID', 'id')" class="finding-card"><div><span class="pill" :class="statusClass(field(item, 'Severity', 'severity'))">{{ field(item, 'Severity', 'severity') }}</span><code>{{ field(item, 'Code', 'code') }}</code></div><h3>{{ field(item, 'Message', 'message') }}</h3><p class="mono">Trace: {{ field(item, 'TraceKey', 'trace_key') }}</p><small>出现 {{ field(item, 'Count', 'count') || 1 }} 次</small></article></div>
            <button v-if="findingCursor" class="load-more" @click="loadFindings(false)">加载更多 Finding</button>
          </article>
        </section>

        <section v-else-if="activeTab === 'appLogs'" class="content-stack">
          <article class="panel search-panel"><div class="panel-heading"><div><h2>App 日志</h2><p>仅索引客户端已脱敏文本。</p></div><span>{{ appLogTotal.toLocaleString() }} 行</span></div><div class="search-row"><input v-model="appLogKeyword" placeholder="关键字" @keyup.enter="searchAppLogs(true)" /><select v-model="appLogSeverity"><option value="">全部级别</option><option value="error">Error</option><option value="warning">Warning</option><option value="info">Info</option></select><button class="button primary" @click="searchAppLogs(true)">检索</button></div></article>
          <article class="panel log-stream"><div v-if="!appLogs.length" class="empty">没有匹配的 App 日志</div><div v-for="line in appLogs" :key="field(line, 'ID', 'id')" class="log-line"><time>{{ field(line, 'TimestampText', 'timestamp_text') || '—' }}</time><span class="pill" :class="statusClass(field(line, 'Severity', 'severity'))">{{ field(line, 'Severity', 'severity') || 'log' }}</span><pre>{{ field(line, 'Message', 'message') }}</pre></div><button v-if="appLogCursor" class="load-more" @click="searchAppLogs(false)">加载更多日志</button></article>
        </section>

        <section v-else class="future-grid">
          <article class="panel future-card"><span>下一阶段</span><h2>调查案例库</h2><p>持久脱敏证据快照、状态机、版本关联和修复后复验后端尚未完成，因此本界面不会伪造案例数据。</p></article>
          <article class="panel future-card"><span>人工门禁</span><h2>AI 证据包</h2><p>结构化导出与导入将在 case store 完成后启用。分析器不会直接调用 AI、修改代码或运行外部命令。</p></article>
        </section>
      </template>
    </main>

    <aside v-if="selectedEvent" class="drawer-backdrop" @click.self="selectedEvent = null">
      <section class="drawer"><header><div><span class="eyebrow">EVENT DETAIL</span><h2>{{ field(selectedEvent, 'Event', 'event') }}</h2></div><button @click="selectedEvent = null">×</button></header><dl>
        <template v-for="key in ['Timestamp','ProjectID','AppSessionID','ConversationID','TurnID','TraceID','SpanID','ParentSpanID','Capability','Operation','Direction','Route','ExecutionTarget','Status','SemanticOutcome','ImplementationState','ErrorCategory','DurationMS','RequestBytes','ResponseBytes']" :key="key"><dt>{{ key }}</dt><dd class="mono">{{ field(selectedEvent, key) ?? '—' }}</dd></template>
      </dl><button v-if="field(selectedEvent, 'PayloadRef', 'payload_ref')" class="button warning-button wide" :disabled="payloadBusy" @click="readPayload">{{ payloadBusy ? '正在读取…' : '显式读取敏感 payload' }}</button><div v-if="payload" class="payload-box"><strong>敏感正文，仅本次查看</strong><pre>{{ field(payload, 'Content', 'content') }}</pre></div></section>
    </aside>
  </div>
</template>