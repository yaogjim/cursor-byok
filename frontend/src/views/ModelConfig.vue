<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import ContentModal from "@/components/ui/ContentModal.vue";
import ModelAdapterTestCard from "@/components/ModelAdapterTestCard.vue";
import ModelEditor from "@/components/ModelEditor.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import { useConfigTransfer } from "@/composables/useConfigTransfer";
import Sortable from "sortablejs";
import {
  appState,
  configSectionDirty,
  configSectionStatusText,
  createEmptyModelAdapter,
  deleteModelAdapterAt,
  duplicateModelAdapterAt,
  getModelAdapterTestResultByID,
  persistScopedUserConfig,
  reloadUserConfig,
  runModelAdapterTest,
  startModelAdapterTest,
  toUserError,
} from "@/state/appState";
import {
  LOGICAL_ROUTING_RUNTIME_VERIFY_HINT,
  selectAdaptersForEndpointTest,
} from "@/state/configProjection";
import {
  filterModelAdapters,
  modelProviderKey,
  modelProviderLabel,
  modelProviderMeta,
  modelProviderTabs,
} from "@/state/modelCatalog";
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";

const BATCH_TEST_CONCURRENCY = 10;
const message = useMessage();

const typeTabs = computed(() => modelProviderTabs(appState.modelAdapters));

const activeType = ref("all");
const searchQuery = ref("");
const layoutMode = ref("list");
const batchTesting = ref(false);
const batchStopping = ref(false);
const batchTotal = ref(0);
const batchCompleted = ref(0);
const editorOpen = ref(false);
const editorIndex = ref(-1);
const editorAdapter = ref(null);
const editorSession = ref(0);
const modelGrid = ref(null);
const modelList = ref(null);
const batchActiveCalls = new Set();
let batchStopRequested = false;
let sortable = null;

const filteredAdapters = computed(() => (
  filterModelAdapters(appState.modelAdapters, {
    type: activeType.value,
    query: searchQuery.value,
  })
));
const filteredAdapterOrderKey = computed(() =>
  filteredAdapters.value.map((adapter) => adapter.id).join("\n"),
);
const batchButtonText = computed(() => {
  if (batchStopping.value) {
    return "停止中...";
  }
  if (!batchTesting.value) {
    return "测试全部";
  }
  return `停止测试 ${batchCompleted.value}/${batchTotal.value}`;
});
const editorTitle = computed(() => (editorIndex.value >= 0 ? "编辑模型配置" : "新增模型配置"));
const modelsFooterStatus = computed(() => {
  const count = (appState.modelAdapters || []).length;
  return `${configSectionStatusText("models")} · 共 ${count} 个模型`;
});
const emptyStateText = computed(() => {
  if (searchQuery.value.trim()) {
    return "没有匹配当前搜索的模型。";
  }
  return activeType.value === "all"
    ? "当前还没有配置任何模型。"
    : `当前还没有配置任何 ${modelProviderLabel(activeType.value)} 模型。`;
});

watch(
  typeTabs,
  (tabs) => {
    if (
      activeType.value === "all"
      || tabs.some((tab) => tab.value === activeType.value && tab.count > 0)
    ) {
      return;
    }
    activeType.value = "all";
  },
  { immediate: true },
);

function showActionError(title, error) {
  const detail = String(error || "服务错误").trim() || "服务错误";
  message(`${title}：${detail}`);
}

const {
  configTransferBusy,
  handleExportConfig,
  handleImportConfig,
} = useConfigTransfer({ message, showActionError });

function maskSecret(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "-";
  }
  if (text.length <= 8) {
    return `${"*".repeat(Math.max(text.length - 2, 0))}${text.slice(-2)}`;
  }
  return `${text.slice(0, 4)}****${text.slice(-4)}`;
}

function providerStyle(adapter) {
  return { "--provider-color": modelProviderMeta(adapter).color };
}

function formatHost(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "-";
  }
  try {
    const parsed = new URL(text);
    return parsed.host || text;
  } catch {
    return text.replace(/^https?:\/\//, "");
  }
}

function openEditor(index = -1) {
  editorIndex.value = index;
  editorAdapter.value = index >= 0
    ? appState.modelAdapters[index]
    : {
        ...createEmptyModelAdapter(),
        type: activeType.value === "anthropic" ? "anthropic" : "openai",
      };
  editorSession.value += 1;
  editorOpen.value = true;
}

function closeEditor() {
  if (appState.configSaving) {
    return;
  }
  editorOpen.value = false;
}

function handleEditorSaved(adapter) {
  if (activeType.value !== "all" && adapter) {
    activeType.value = modelProviderKey(adapter);
  }
}

function destroySortable() {
  if (!sortable) {
    return;
  }
  sortable.destroy();
  sortable = null;
}

function syncSortable() {
  const element = layoutMode.value === "list" ? modelList.value : modelGrid.value;
  if (!element) {
    destroySortable();
    return;
  }
  if (!sortable || sortable.el !== element) {
    destroySortable();
    sortable = Sortable.create(element, {
      animation: 160,
      dataIdAttr: "data-model-id",
      draggable: ".model-sort-item",
      handle: ".model-sort-handle",
      ghostClass: "opacity-40",
      chosenClass: "!border-[#10AD5D]",
      dragClass: "cursor-grabbing",
      onEnd: (event) => {
        void handleModelSort(event);
      },
    });
  }
  sortable.option(
    "disabled",
    appState.configSaving || batchTesting.value || Boolean(searchQuery.value.trim()),
  );
  sortable.sort(filteredAdapters.value.map((adapter) => adapter.id), false);
}

function handleModelSort(event) {
  const oldIndex = event.oldDraggableIndex ?? event.oldIndex;
  const newIndex = event.newDraggableIndex ?? event.newIndex;
  if (
    !Number.isInteger(oldIndex)
    || !Number.isInteger(newIndex)
    || oldIndex === newIndex
  ) {
    syncSortable();
    return;
  }

  const reorderedTypeAdapters = filteredAdapters.value.slice();
  const [movedAdapter] = reorderedTypeAdapters.splice(oldIndex, 1);
  if (!movedAdapter || newIndex < 0 || newIndex > reorderedTypeAdapters.length) {
    syncSortable();
    return;
  }
  reorderedTypeAdapters.splice(newIndex, 0, movedAdapter);

  const previousAdapters = appState.modelAdapters.slice();
  let nextAdapters = reorderedTypeAdapters;
  if (activeType.value !== "all") {
    let typeIndex = 0;
    nextAdapters = previousAdapters.map((adapter) => {
      if (modelProviderKey(adapter) !== activeType.value) {
        return adapter;
      }
      const nextAdapter = reorderedTypeAdapters[typeIndex];
      typeIndex += 1;
      return nextAdapter;
    });
  }
  nextAdapters = nextAdapters
    .map((adapter, index) => ({
      ...adapter,
      sort: index + 1,
    }));

  appState.modelAdapters = nextAdapters;
  void nextTick().then(syncSortable);
}

watch(
  () => [
    modelGrid.value,
    modelList.value,
    layoutMode.value,
    activeType.value,
    searchQuery.value,
    filteredAdapterOrderKey.value,
    appState.configSaving,
    batchTesting.value,
  ],
  () => {
    void nextTick().then(syncSortable);
  },
  { flush: "post" },
);

async function handleClearAllModelAdapters() {
  const count = appState.modelAdapters.length;
  if (count === 0) {
    return;
  }
  const confirmed = await showModal({
    title: "清除全部模型",
    content: `确定移除当前 ${count} 个模型配置吗？移除后需要保存本页才会写入配置。`,
    confirmText: "清除全部",
    cancelText: "取消",
  });
  if (!confirmed) {
    return;
  }
  appState.modelAdapters = [];
  activeType.value = "all";
}

async function handleDeleteModelAdapter(index) {
  const target = appState.modelAdapters[index];
  if (!target) {
    showActionError("删除失败", "模型配置不存在，无法删除");
    return;
  }
  const confirmed = await showModal({
    title: "删除模型配置",
    content: `确定删除「${target.displayName || target.modelID || "模型"}」吗？删除后需要保存本页才会写入配置。`,
    confirmText: "删除",
    cancelText: "取消",
  });
  if (!confirmed) {
    return;
  }
  const result = deleteModelAdapterAt(index);
  if (!result.ok) {
    showActionError("删除失败", result.error);
  }
}

function handleDuplicateModelAdapter(index) {
  const target = appState.modelAdapters[index];
  if (!target) {
    showActionError("复制失败", "模型配置不存在，无法复制");
    return;
  }
  const result = duplicateModelAdapterAt(index);
  if (!result.ok) {
    showActionError("复制失败", result.error);
  }
}

async function handleSavePage() {
  const result = await persistScopedUserConfig("models");
  if (!result.ok) {
    showActionError("保存失败", result.error);
    return;
  }
  message("本页配置已保存");
}

async function handleReloadPage() {
  try {
    await reloadUserConfig({ modelAdaptersOnly: true });
  } catch (error) {
    showActionError("重新加载失败", toUserError(error));
  }
}

function getAdapterTestResult(adapter) {
  return getModelAdapterTestResultByID(adapter?.id);
}

function adapterEndpoint(adapter) {
  if (adapter?.type === "openai") {
    return adapter.openAIEndpoint || "/v1/responses";
  }
  return formatHost(adapter?.baseURL);
}

function testStatusMeta(adapter) {
  const result = getAdapterTestResult(adapter);
  const status = String(result?.status || "").trim().toLowerCase();
  if (status === "running") {
    return { label: "测试中", tone: "info" };
  }
  if (status === "success") {
    return { label: result?.summaryText || "通过", tone: "ok" };
  }
  if (status === "error") {
    return { label: result?.summaryText || "失败", tone: "err" };
  }
  return { label: "未测试", tone: "off" };
}

function isAdapterTesting(adapter) {
  return getAdapterTestResult(adapter)?.status === "running";
}

async function handleTestModelAdapter(adapter) {
  const plan = selectAdaptersForEndpointTest([adapter]);
  if (plan.skippedLogical.length) {
    message(LOGICAL_ROUTING_RUNTIME_VERIFY_HINT);
    return;
  }
  try {
    await runModelAdapterTest(plan.toTest[0] ?? adapter);
  } catch (_error) {
    // 失败结果会通过事件同步到界面，这里不再额外弹窗打断用户。
  }
}

function isCancelError(error) {
  return String(error?.name || "").trim() === "CancelError";
}

async function stopBatchTesting() {
  if (!batchTesting.value || batchStopping.value) {
    return;
  }
  batchStopRequested = true;
  batchStopping.value = true;
  const activeCalls = Array.from(batchActiveCalls);
  await Promise.allSettled(
    activeCalls.map((call) => (typeof call?.cancel === "function" ? call.cancel("batch-stop") : undefined)),
  );
}

async function handleTestAllModelAdapters() {
  if (batchTesting.value) {
    await stopBatchTesting();
    return;
  }
  const plan = selectAdaptersForEndpointTest(filteredAdapters.value);
  const adapters = plan.toTest.slice();
  if (adapters.length === 0) {
    return;
  }
  batchStopRequested = false;
  batchTesting.value = true;
  batchStopping.value = false;
  batchTotal.value = adapters.length;
  batchCompleted.value = 0;
  let nextIndex = 0;
  try {
    const workers = Array.from({ length: Math.min(BATCH_TEST_CONCURRENCY, adapters.length) }, async () => {
      while (!batchStopRequested) {
        const currentIndex = nextIndex;
        nextIndex += 1;
        if (currentIndex >= adapters.length) {
          return;
        }
        const adapter = adapters[currentIndex];
        const call = startModelAdapterTest(adapter);
        batchActiveCalls.add(call);
        try {
          await call;
        } catch (error) {
          if (!isCancelError(error) && !batchStopRequested) {
            // 单个失败结果由卡片自行展示，这里继续后续测试。
          }
        } finally {
          batchActiveCalls.delete(call);
          batchCompleted.value += 1;
        }
      }
    });
    await Promise.allSettled(workers);
  } finally {
    batchActiveCalls.clear();
    batchStopRequested = false;
    batchTesting.value = false;
    batchStopping.value = false;
  }
}

onMounted(async () => {
  await reloadUserConfig({ modelAdaptersOnly: true }).catch(() => { });
  await nextTick();
  syncSortable();
});

onBeforeUnmount(() => {
  void stopBatchTesting();
  destroySortable();
});
</script>

<template>
  <div class="page-shell page-shell--fill gap-3 text-[var(--color-text)]">
    <div class="page-title-row">
      <div class="page-title-block">
        <h2 class="page-title">模型管理</h2>
      </div>
    </div>

    <Card :padded="false" class="compact-card min-h-0 flex-1">
      <div class="ui-card-body shrink-0 pb-3">
        <div class="page-toolbar">
          <label class="search-field">
            <span class="icon icon-[mdi--magnify]" aria-hidden="true" />
            <input
              v-model="searchQuery"
              type="search"
              class="h-8 w-full rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] pr-3 text-sm text-[var(--color-text)] outline-none placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-primary)]"
              placeholder="搜索别名、上游 ID 或端点"
              aria-label="搜索模型"
            >
          </label>
          <div class="center-row spread flex-wrap gap-2">
            <Button
              variant="default"
              class="btn-sm"
              :disabled="appState.configSaving || batchTesting || configTransferBusy || appState.serviceRunning || appState.backendRunning || appState.proxyRunning"
              title="导入完整配置"
              @click="handleImportConfig"
            >
              导入
            </Button>
            <Button
              variant="default"
              class="btn-sm"
              :disabled="appState.configSaving || batchTesting || configTransferBusy"
              title="导出完整配置"
              @click="handleExportConfig"
            >
              导出
            </Button>
            <Button
              variant="default"
              class="btn-sm"
              :disabled="appState.configSaving || batchTesting || configTransferBusy || appState.modelAdapters.length === 0"
              @click="handleClearAllModelAdapters"
            >
              清除全部
            </Button>
            <Button variant="primary" class="btn-sm" :disabled="appState.configSaving || batchTesting || configTransferBusy" @click="openEditor()">
              <span class="icon-[mdi--plus] text-[14px]" aria-hidden="true" />
              新增模型
            </Button>
          </div>
        </div>

        <div class="mt-3 flex flex-wrap items-center gap-2">
          <div class="seg min-w-0 overflow-x-auto" role="tablist" aria-label="按提供商筛选">
            <button
              v-for="tab in typeTabs"
              :key="tab.value"
              type="button"
              role="tab"
              :aria-selected="activeType === tab.value"
              :class="activeType === tab.value ? 'is-on' : ''"
              @click="activeType = tab.value"
            >
              <span
                v-if="tab.value !== 'all'"
                class="model-provider-dot"
                :style="{ '--provider-color': tab.color }"
                aria-hidden="true"
              />
              {{ tab.label }}
              <span class="opacity-55">{{ tab.count }}</span>
            </button>
          </div>
          <div class="seg spread" role="group" aria-label="模型视图">
            <button
              type="button"
              title="列表视图"
              aria-label="列表视图"
              :aria-pressed="layoutMode === 'list'"
              :class="layoutMode === 'list' ? 'is-on' : ''"
              @click="layoutMode = 'list'"
            >
              <span class="icon-[mdi--format-list-bulleted] text-[14px]" aria-hidden="true" />
            </button>
            <button
              type="button"
              title="栅格视图"
              aria-label="栅格视图"
              :aria-pressed="layoutMode === 'grid'"
              :class="layoutMode === 'grid' ? 'is-on' : ''"
              @click="layoutMode = 'grid'"
            >
              <span class="icon-[mdi--view-grid-outline] text-[14px]" aria-hidden="true" />
            </button>
          </div>
        </div>
      </div>

      <div
        v-if="filteredAdapters.length === 0"
        class="compact-card-body flex items-center justify-center text-sm text-[var(--color-text-secondary)]"
      >
        {{ emptyStateText }}
      </div>

      <div
        v-else
        class="compact-card-body compact-card-body--flush"
        :class="layoutMode === 'grid' ? '!p-4' : ''"
      >
        <div v-show="layoutMode === 'list'">
          <div class="model-row model-head" aria-hidden="true">
            <div>别名 / 上游 ID</div>
            <div>提供商</div>
            <div>测试状态</div>
            <div>端点</div>
            <div>操作</div>
          </div>
          <div ref="modelList">
            <div
              v-for="(adapter, index) in filteredAdapters"
              :key="adapter.id || `${adapter.baseURL}-${adapter.modelID}-${index}`"
              class="model-row model-sort-item group"
              :data-model-id="adapter.id"
            >
              <div class="flex min-w-0 items-start gap-2">
                <button
                  type="button"
                  class="model-sort-handle mt-0.5 center-row h-[28px] w-[28px] shrink-0 touch-none cursor-grab justify-center rounded-[6px] text-[var(--color-text-muted)] outline-none hover:bg-[var(--color-surface-muted)] hover:text-[var(--color-text)] disabled:cursor-not-allowed disabled:opacity-30"
                  :disabled="appState.configSaving || batchTesting || Boolean(searchQuery.trim())"
                  aria-label="拖拽排序"
                  title="拖拽排序"
                  @click.stop
                >
                  <span class="icon-[icon-park-outline--drag] text-[18px]" />
                </button>
                <div class="min-w-0">
                  <div class="truncate font-semibold">{{ adapter.displayName }}</div>
                  <div class="truncate font-mono text-[11px] text-[var(--color-text-muted)]">{{ adapter.modelID }}</div>
                </div>
              </div>
              <div
                class="model-provider-chip"
                :style="providerStyle(adapter)"
              >
                <i aria-hidden="true" />
                {{ modelProviderMeta(adapter).label }}
              </div>
              <div>
                <span
                  class="inline-flex h-[26px] items-center rounded-full px-2.5 text-[11px] font-semibold"
                  :class="{
                    'bg-[var(--color-success-bg)] text-[var(--color-success-text)]': testStatusMeta(adapter).tone === 'ok',
                    'bg-[var(--color-error-bg)] text-[var(--color-error-text)]': testStatusMeta(adapter).tone === 'err',
                    'bg-[var(--color-info-bg)] text-[var(--color-info-text)]': testStatusMeta(adapter).tone === 'info',
                    'bg-[var(--color-surface-muted)] text-[var(--color-text-muted)]': testStatusMeta(adapter).tone === 'off',
                  }"
                >
                  {{ testStatusMeta(adapter).label }}
                </span>
              </div>
              <div>
                <span class="rounded-[6px] bg-[var(--color-surface-muted)] px-2 py-0.5 font-mono text-[11px] text-[var(--color-text-secondary)]">
                  {{ adapterEndpoint(adapter) }}
                </span>
              </div>
              <div class="center-row flex-wrap justify-end gap-1">
                <Button
                  variant="default"
                  class="btn-sm"
                  :disabled="appState.configSaving || batchTesting || isAdapterTesting(adapter)"
                  @click="handleTestModelAdapter(adapter)"
                >
                  {{ isAdapterTesting(adapter) ? "测试中..." : "测试" }}
                </Button>
                <Button variant="default" class="btn-sm" :disabled="appState.configSaving" @click="openEditor(appState.modelAdapters.indexOf(adapter))">编辑</Button>
                <details class="model-more-menu">
                  <summary aria-label="更多操作" title="更多操作">
                    <span class="icon-[mdi--dots-vertical] text-[16px]" aria-hidden="true" />
                  </summary>
                  <div class="model-more-menu-popover">
                    <button type="button" :disabled="appState.configSaving" @click="handleDuplicateModelAdapter(appState.modelAdapters.indexOf(adapter))">复制</button>
                    <button type="button" class="is-risk" :disabled="appState.configSaving" @click="handleDeleteModelAdapter(appState.modelAdapters.indexOf(adapter))">删除</button>
                  </div>
                </details>
              </div>
            </div>
          </div>
        </div>

        <div
          v-show="layoutMode === 'grid'"
          ref="modelGrid"
          class="grid gap-4 pb-1 [grid-template-columns:repeat(auto-fill,minmax(280px,1fr))]"
        >
          <Card
            v-for="(adapter, index) in filteredAdapters"
            :key="adapter.id || `${adapter.baseURL}-${adapter.modelID}-${index}`"
            class="model-sort-item group relative flex flex-col"
            :data-model-id="adapter.id"
          >
            <button
              type="button"
              class="model-sort-handle absolute left-2 top-2 z-10 center-row h-[30px] w-[30px] shrink-0 touch-none cursor-grab justify-center rounded-[6px] border border-transparent bg-transparent text-transparent opacity-0 outline-none transition-[opacity,color,border-color,background-color] focus-visible:border-[#10AD5D] focus-visible:bg-[#333333] focus-visible:text-white focus-visible:opacity-100 active:cursor-grabbing group-hover:border-[#454545] group-hover:bg-[#333333] group-hover:text-white group-hover:opacity-100 disabled:cursor-not-allowed disabled:opacity-30"
              :disabled="appState.configSaving || batchTesting || Boolean(searchQuery.trim())"
              aria-label="拖拽排序"
              title="拖拽排序"
              @click.stop
            >
              <span class="icon-[icon-park-outline--drag] text-[20px]" />
            </button>
            <div class="flex flex-1 flex-col gap-3">
              <div class="flex min-w-0 flex-1 flex-col gap-2.5">
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0 flex-1">
                    <div class="truncate text-base font-bold text-[var(--color-text)]">{{ adapter.displayName }}</div>
                    <div class="mt-1 truncate text-sm text-[var(--color-text-secondary)]">{{ adapter.modelID }}</div>
                    <div v-if="adapter.type === 'openai'" class="mt-0.5 truncate text-xs text-[var(--color-text-muted)]">
                      {{ adapter.openAIEndpoint || "/v1/responses" }}
                    </div>
                  </div>
                  <span
                    class="model-provider-chip"
                    :style="providerStyle(adapter)"
                  >
                    <i aria-hidden="true" />
                    <span>{{ modelProviderMeta(adapter).label }}</span>
                  </span>
                </div>

                <ModelAdapterTestCard
                  compact
                  title="测试"
                  empty-text="未测试"
                  :result="getAdapterTestResult(adapter)"
                />
              </div>

              <div class="center-row shrink-0 flex-wrap justify-end gap-2 border-t border-[var(--color-border)] pt-3">
                <Button
                  variant="default"
                  class="btn-sm"
                  :disabled="appState.configSaving || batchTesting || isAdapterTesting(adapter)"
                  @click="handleTestModelAdapter(adapter)"
                >
                  {{ isAdapterTesting(adapter) ? "测试中..." : "测试" }}
                </Button>
                <Button variant="default" class="btn-sm" :disabled="appState.configSaving" @click="openEditor(appState.modelAdapters.indexOf(adapter))">编辑</Button>
                <Button variant="default" class="btn-sm" :disabled="appState.configSaving" @click="handleDuplicateModelAdapter(appState.modelAdapters.indexOf(adapter))">复制</Button>
                <Button variant="text" class="btn-sm" :disabled="appState.configSaving"
                  @click="handleDeleteModelAdapter(appState.modelAdapters.indexOf(adapter))">删除</Button>
              </div>
            </div>
          </Card>
        </div>
      </div>
      <div class="config-action-bar">
        <span
          class="config-action-status"
          :class="configSectionDirty.models ? 'is-dirty' : ''"
        >
          {{ modelsFooterStatus }}
        </span>
        <div class="config-action-buttons spread">
          <Button
            variant="text"
            class="btn-sm"
            :disabled="appState.configSaving || batchTesting || configTransferBusy"
            @click="handleReloadPage"
          >
            重新加载
          </Button>
          <Button
            variant="primary"
            class="btn-sm"
            :disabled="appState.configSaving || batchTesting || configTransferBusy || !configSectionDirty.models"
            @click="handleSavePage"
          >
            {{ appState.configSaving ? "保存中..." : "保存模型配置" }}
          </Button>
        </div>
      </div>
    </Card>
  </div>

  <ContentModal
    :open="editorOpen"
    :title="editorTitle"
    size="xl"
    :close-disabled="appState.configSaving"
    @close="closeEditor"
  >
    <ModelEditor
      v-if="editorOpen && editorAdapter"
      :key="editorSession"
      :index="editorIndex"
      :adapter="editorAdapter"
      @saved="handleEditorSaved"
      @close="closeEditor"
    />
  </ContentModal>
</template>
