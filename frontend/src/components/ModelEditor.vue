<script setup>
import Button from "@/components/ui/Button.vue";
import Combobox from "@/components/ui/Combobox.vue";
import Input from "@/components/ui/Input.vue";
import ModelAdapterTestCard from "@/components/ModelAdapterTestCard.vue";
import Select from "@/components/ui/Select.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { useMessage } from "@/composables/useMessage";
import {
  ANTHROPIC_THINKING_EFFORT_DEFAULT,
  appState,
  buildModelAdapterTestRequestHash,
  createEmptyModelAdapter,
  CUSTOM_HEADERS_DEFAULT_JSON,
  EXTRA_PARAMS_DEFAULT_JSON,
  fetchAvailableModelIDs,
  getModelAdapterTestResult,
  getModelAdapterTestResultByID,
  isModelAdapterTestResultStale,
  normalizeModelAdapter,
  OPENAI_ENDPOINT_CHAT_COMPLETIONS,
  OPENAI_ENDPOINT_CUSTOM,
  OPENAI_ENDPOINT_RESPONSES,
  OPENAI_EXTRA_PARAMS_DEFAULT_JSON,
  runModelAdapterTest,
  saveModelAdapterAt,
  toUserError,
  validateModelAdapters,
} from "@/state/appState";
import { computed, onBeforeUnmount, reactive, ref, watch } from "vue";

const modelTypeTabs = [
  { label: "OpenAI", value: "openai", icon: "icon-[bxl--openai]" },
  { label: "Anthropic", value: "anthropic", icon: "icon-[logos--claude-icon]" },
];

const reasoningEffortOptions = [
  { label: "不设置", value: "", icon: "icon-[mdi--minus-circle-outline]" },
  { label: "低", value: "low", icon: "icon-[mdi--head-outline]" },
  { label: "中", value: "medium", icon: "icon-[mdi--head-lightbulb-outline]" },
  { label: "高", value: "high", icon: "icon-[mdi--brain]" },
  { label: "极高", value: "xhigh", icon: "icon-[mdi--head-cog-outline]" },
  { label: "最高", value: "max", icon: "icon-[mdi--brain]" },
];

const anthropicThinkingEffortOptions = [
  { label: "低", value: "low", icon: "icon-[mdi--head-outline]" },
  { label: "中", value: "medium", icon: "icon-[mdi--head-lightbulb-outline]" },
  { label: "高", value: "high", icon: "icon-[mdi--brain]" },
  { label: "极高", value: "xhigh", icon: "icon-[mdi--head-cog-outline]" },
  { label: "Max", value: "max", icon: "icon-[mdi--brain]" },
];

const openAIEndpointOptions = [
  { label: "/v1/responses", value: OPENAI_ENDPOINT_RESPONSES, icon: "icon-[mdi--api]" },
  { label: "/v1/chat/completions", value: OPENAI_ENDPOINT_CHAT_COMPLETIONS, icon: "icon-[mdi--message-text-outline]" },
  { label: "自定义路径(请输入完整请求地址)", value: OPENAI_ENDPOINT_CUSTOM, icon: "icon-[mdi--pencil-outline]" },
];

const props = defineProps({
  index: { type: Number, default: -1 },
  adapter: { type: Object, default: () => createEmptyModelAdapter() },
});

const emit = defineEmits(["close", "saved"]);
const message = useMessage();

const editorIndex = ref(props.index);
const draft = reactive(normalizeModelAdapter(props.adapter));
if (!draft.type) {
  draft.type = "openai";
}
const lastTestAdapterID = ref("");
const localTestFailure = ref("");
const availableModelIDs = ref(draft.modelID ? [draft.modelID] : []);
const modelListLoading = ref(false);
const modelListRequestSeq = ref(0);
let modelListDebounceTimer = 0;

function createOptionalPositiveIntegerModel(key) {
  return computed({
    get() {
      return draft[key] > 0 ? String(draft[key]) : "";
    },
    set(value) {
      const text = String(value || "").trim();
      draft[key] = /^\d+$/.test(text) && Number(text) > 0 ? Number(text) : 0;
    },
  });
}

const maxCompletionTokensInput = createOptionalPositiveIntegerModel("maxCompletionTokens");
const anthropicMaxTokensInput = createOptionalPositiveIntegerModel("anthropicMaxTokens");
const contextWindowTokensInput = createOptionalPositiveIntegerModel("contextWindowTokens");
const interfacePlaceholder = computed(() =>
  draft.type === "anthropic" ? "例如：https://api.anthropic.com" : "例如：https://api.openai.com/v1",
);
const modelOptions = computed(() => availableModelIDs.value.map((modelID) => ({
  label: modelID,
  value: modelID,
  icon: "icon-[mdi--cube-outline]",
})));
const canFetchModels = computed(() => Boolean(
  draft.type && String(draft.baseURL || "").trim() && String(draft.apiKey || "").trim(),
));
const selectedTestAdapter = computed(() => normalizeModelAdapter(draft));
const currentRequestHash = computed(() => buildModelAdapterTestRequestHash(selectedTestAdapter.value));
const directModelTestResult = computed(() => getModelAdapterTestResult(selectedTestAdapter.value));
const rememberedModelTestResult = computed(() =>
  lastTestAdapterID.value ? getModelAdapterTestResultByID(lastTestAdapterID.value) : null,
);
const activeModelTestResult = computed(() => directModelTestResult.value || rememberedModelTestResult.value);
const modelTestResultStale = computed(() =>
  isModelAdapterTestResultStale(selectedTestAdapter.value, activeModelTestResult.value),
);
const isCurrentConfigTesting = computed(() => directModelTestResult.value?.status === "running");
const modelTestSummary = computed(() => {
  if (localTestFailure.value) {
    return localTestFailure.value;
  }
  return activeModelTestResult.value?.summaryText || "尚未测试";
});

function ensureOpenAIExtraParamsJSON() {
  if (!String(draft.openAIExtraParamsJSON || "").trim()) {
    draft.openAIExtraParamsJSON = OPENAI_EXTRA_PARAMS_DEFAULT_JSON;
  }
}

function ensureCustomHeadersJSON() {
  if (!String(draft.customHeadersJSON || "").trim()) {
    draft.customHeadersJSON = CUSTOM_HEADERS_DEFAULT_JSON;
  }
}

function ensureAnthropicExtraParamsJSON() {
  if (!String(draft.anthropicExtraParamsJSON || "").trim()) {
    draft.anthropicExtraParamsJSON = EXTRA_PARAMS_DEFAULT_JSON;
  }
}

function ensureAnthropicThinkingEffort() {
  if (!String(draft.anthropicThinkingEffort || "").trim()) {
    draft.anthropicThinkingEffort = ANTHROPIC_THINKING_EFFORT_DEFAULT;
  }
}

const fieldTips = {
  displayName: "仅用于界面展示，便于你区分不同模型。",
  modelID: "可以直接输入模型标识，或从服务端返回的列表中选择。",
  baseURL: "模型服务的 API 根地址，通常为兼容 OpenAI 或 Anthropic 的接口入口。",
  apiKey: "调用该模型服务需要使用的访问密钥。",
  contextWindowTokens: "模型单次可接受的最大上下文 Token 数。留空时使用默认值。",
  reasoningEffort: "仅当模型支持 reasoning_effort 时才选择推理强度；选择“不设置”后，请求不会携带该参数。越高通常越稳，但也可能更慢。",
  maxCompletionTokens: "单次回复允许生成的最大 Token 数。留空时使用默认值。",
  openAIEndpoint: "选择接口协议端点。选“自定义路径”时，请在接口地址栏填写完整请求地址（含 /chat/completions 或 /responses 路径后缀），系统会根据末段自动判断协议形态。",
  openAIExtraParams: "开启后会把 JSON 对象覆盖到 OpenAI 请求体。同名字段以这里为准。OpenAI service_tier 支持 auto、default、flex、scale、priority。",
  customHeaders: "开启后会把 JSON 对象覆盖到最终请求头。同名请求头以这里为准，值必须是字符串。",
  anthropicExtraParams: "开启后会把 JSON 对象覆盖到 Anthropic 请求体。同名字段以这里为准。",
  anthropicMaxTokens: "Anthropic 模型单次回复允许生成的最大 Token 数。留空时使用默认值。",
  anthropicThinkingEffort: "Anthropic adaptive thinking 的思考强度。请求会固定使用新版 thinking.type=adaptive。",
  providerFallback: "启用后，此模型路由的请求将依次尝试主渠道和备选渠道，而非使用当前渠道本身。所有渠道共享同一总 attempt 与等待预算；仅在零原始字节、零 model event 时才允许渠道切换；一旦开始输出则禁止回退。",
  tooltipData: "模型列表 hover 时显示的备注说明。",
};

async function refreshModelList() {
  const baseURL = String(draft.baseURL || "").trim();
  const apiKey = String(draft.apiKey || "").trim();
  if (!baseURL || !apiKey || !draft.type) {
    modelListRequestSeq.value += 1;
    availableModelIDs.value = [];
    modelListLoading.value = false;
    return [];
  }

  const requestSeq = modelListRequestSeq.value + 1;
  modelListRequestSeq.value = requestSeq;
  modelListLoading.value = true;
  availableModelIDs.value = [];
  try {
    const models = await fetchAvailableModelIDs({
      type: draft.type,
      baseURL,
      apiKey,
      customHeadersEnabled: draft.customHeadersEnabled,
      customHeadersJSON: draft.customHeadersJSON,
    });
    if (requestSeq !== modelListRequestSeq.value) {
      return availableModelIDs.value;
    }
    availableModelIDs.value = models;
    return models;
  } catch (_error) {
    if (requestSeq === modelListRequestSeq.value) {
      availableModelIDs.value = [];
    }
    return availableModelIDs.value;
  } finally {
    if (requestSeq === modelListRequestSeq.value) {
      modelListLoading.value = false;
    }
  }
}

async function persistDraft() {
  const adapter = normalizeModelAdapter(draft);

  const singleCheck = validateModelAdapters([adapter], { allAdapters: appState.modelAdapters });
  if (singleCheck) {
    message(singleCheck);
    return { ok: false, error: singleCheck, adapter: null };
  }

  const result = await saveModelAdapterAt(editorIndex.value, adapter);
  if (!result.ok) {
    message(result.error);
    return { ok: false, error: result.error, adapter: null };
  }

  if (typeof result.index === "number") {
    editorIndex.value = result.index;
  }
  if (result.adapter) {
    Object.assign(draft, normalizeModelAdapter(result.adapter));
  } else {
    Object.assign(draft, adapter);
  }
  return {
    ok: true,
    error: "",
    adapter: result.adapter ? normalizeModelAdapter(result.adapter) : normalizeModelAdapter(adapter),
  };
}

async function handleSave() {
  const result = await persistDraft();
  if (!result.ok) {
    return;
  }
  emit("saved", result.adapter);
  emit("close");
}

function handleCancel() {
  emit("close");
}

function handleModelTypeChange(type) {
  draft.type = type;
  modelListRequestSeq.value += 1;
  modelListLoading.value = false;
  availableModelIDs.value = [];
  draft.modelID = "";
  if (type === "openai" && !draft.openAIEndpoint) {
    draft.openAIEndpoint = OPENAI_ENDPOINT_CHAT_COMPLETIONS;
  } else if (type === "anthropic") {
    ensureAnthropicThinkingEffort();
  }
}

async function handleTest() {
  localTestFailure.value = "";
  try {
    const saved = await persistDraft();
    if (!saved.ok || !saved.adapter) {
      return;
    }
    const result = await runModelAdapterTest(saved.adapter);
    if (result?.adapterID) {
      lastTestAdapterID.value = result.adapterID;
    }
  } catch (error) {
    const latest = getModelAdapterTestResult(draft);
    if (latest?.adapterID) {
      lastTestAdapterID.value = latest.adapterID;
      return;
    }
    localTestFailure.value = toUserError(error);
  }
}

watch(
  directModelTestResult,
  (result) => {
    if (!result?.adapterID) {
      return;
    }
    lastTestAdapterID.value = result.adapterID;
    if (result.status !== "running") {
      localTestFailure.value = "";
    }
  },
  { immediate: true },
);

watch(currentRequestHash, () => {
  localTestFailure.value = "";
});

watch(
  () => draft.openAIExtraParamsEnabled,
  (enabled) => {
    if (enabled) {
      ensureOpenAIExtraParamsJSON();
    }
  },
);

watch(
  () => draft.customHeadersEnabled,
  (enabled) => {
    if (enabled) {
      ensureCustomHeadersJSON();
    }
  },
);

watch(
  () => draft.anthropicExtraParamsEnabled,
  (enabled) => {
    if (enabled) {
      ensureAnthropicExtraParamsJSON();
    }
  },
);

watch(
  () => [draft.type, draft.baseURL, draft.apiKey, draft.customHeadersEnabled, draft.customHeadersJSON],
  () => {
    window.clearTimeout(modelListDebounceTimer);
    const baseURL = String(draft.baseURL || "").trim();
    const apiKey = String(draft.apiKey || "").trim();
    if (!baseURL || !apiKey) {
      modelListRequestSeq.value += 1;
      modelListLoading.value = false;
      availableModelIDs.value = [];
      return;
    }
    modelListDebounceTimer = window.setTimeout(() => {
      void refreshModelList();
    }, 600);
  },
  { immediate: true },
);

onBeforeUnmount(() => {
  window.clearTimeout(modelListDebounceTimer);
});

// ── providerFallback UI 支持 ──

// 其他已保存渠道（排除当前编辑中的适配器）
const otherAdapters = computed(() =>
  appState.modelAdapters.filter((a) => a.id && a.id !== draft.id),
);

// 渠道下拉基础选项，含空"不选择"项
const fallbackChannelBaseOptions = computed(() => [
  { label: "── 不选择 ──", value: "", icon: "icon-[mdi--minus-circle-outline]" },
  ...otherAdapters.value.map((a) => ({
    label: a.displayName ? `${a.displayName}（${a.modelID}）` : (a.modelID || a.id),
    value: a.id,
    icon: a.type === "anthropic" ? "icon-[logos--claude-icon]" : "icon-[bxl--openai]",
  })),
]);

// 主渠道选项：排除已选候选
const fallbackPrimaryOptions = computed(() =>
  fallbackChannelBaseOptions.value.filter(
    (o) => !o.value || !draft.providerFallback.candidateChannelIDs.includes(o.value),
  ),
);

// 候选渠道1选项：排除主渠道 + 候选2
const fallbackCandidate1Options = computed(() =>
  fallbackChannelBaseOptions.value.filter(
    (o) => !o.value
      || (o.value !== draft.providerFallback.primaryChannelID
        && o.value !== (draft.providerFallback.candidateChannelIDs[1] || "")),
  ),
);

// 候选渠道2选项：排除主渠道 + 候选1
const fallbackCandidate2Options = computed(() =>
  fallbackChannelBaseOptions.value.filter(
    (o) => !o.value
      || (o.value !== draft.providerFallback.primaryChannelID
        && o.value !== (draft.providerFallback.candidateChannelIDs[0] || "")),
  ),
);

// 是否跨 Provider（OpenAI/Anthropic 混用）
const isCrossProviderFallback = computed(() => {
  if (!draft.providerFallback.enabled) return false;
  const ids = [
    draft.providerFallback.primaryChannelID,
    ...(draft.providerFallback.candidateChannelIDs || []),
  ].filter(Boolean);
  if (ids.length < 2) return false;
  const types = new Set(
    ids.map((id) => appState.modelAdapters.find((x) => x.id === id)?.type || "").filter(Boolean),
  );
  return types.size > 1;
});

// 启用时无其他渠道可选
const fallbackNoChannels = computed(() =>
  draft.providerFallback.enabled && otherAdapters.value.length === 0,
);

// 候选渠道1（双向绑定槽位0，清空时同步清槽位1）
const candidate1 = computed({
  get() {
    return draft.providerFallback.candidateChannelIDs[0] ?? "";
  },
  set(val) {
    const v = String(val || "").trim();
    const c2 = draft.providerFallback.candidateChannelIDs[1] ?? "";
    draft.providerFallback.candidateChannelIDs = v ? (c2 ? [v, c2] : [v]) : [];
  },
});

// 候选渠道2（双向绑定槽位1）
const candidate2 = computed({
  get() {
    return draft.providerFallback.candidateChannelIDs[1] ?? "";
  },
  set(val) {
    const v = String(val || "").trim();
    const c1 = draft.providerFallback.candidateChannelIDs[0] ?? "";
    draft.providerFallback.candidateChannelIDs = c1 ? (v ? [c1, v] : [c1]) : [];
  },
});
</script>

<template>
  <div class="flex h-full flex-col text-[var(--color-text)]">
    <div class="flex-shrink-0 p-4" v-if="localTestFailure || activeModelTestResult">
      <ModelAdapterTestCard
        :result="localTestFailure ? { status: 'error', error: '测试失败', summaryText: '测试失败', rawResponse: modelTestSummary } : activeModelTestResult"
        :stale="modelTestResultStale"
        :show-metrics="true"
      />
    </div>
    <div class="flex-1 min-h-0 overflow-y-auto px-4 py-4 scroll-shadow-bottom">
      <div class="flex flex-col gap-4">
        <div class="center-row gap-2">
          <button
            v-for="tab in modelTypeTabs"
            :key="tab.value"
            type="button"
            class="center-row gap-2 rounded-[8px] border px-3 py-2 text-sm transition-colors duration-150"
            :class="draft.type === tab.value
              ? 'border-[var(--color-primary)] bg-[var(--color-success-bg)] text-[var(--color-text)]'
              : 'border-[var(--color-border)] bg-[var(--color-surface-muted)] text-[var(--color-text-secondary)] hover:border-[var(--color-border-strong)] hover:text-[var(--color-text)]'"
            @click="handleModelTypeChange(tab.value)"
          >
            <span :class="[tab.icon, 'text-[16px]']"></span>
            <span>{{ tab.label }}</span>
          </button>
        </div>

        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.baseURL" />
              <span>接口地址</span>
            </span>
            <input
              v-model="draft.baseURL"
              type="text"
              :placeholder="interfacePlaceholder"
              class="h-9 rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-primary)]"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.apiKey" />
              <span>访问密钥</span>
            </span>
            <Input
              v-model="draft.apiKey"
              type="password"
              allow-visibility-toggle
              placeholder="例如：sk-xxxxxx"
              autocomplete="off"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.displayName" />
              <span>显示名称</span>
            </span>
            <input
              v-model="draft.displayName"
              type="text"
              placeholder="例如：GPT-5"
              class="h-9 rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-primary)]"
            />
          </label>

          <div class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.modelID" />
              <span>模型标识</span>
            </span>
            <Combobox
              v-model="draft.modelID"
              :options="modelOptions"
              :loading="modelListLoading"
              placeholder="例如：gpt-4.1"
              empty-text="没有匹配的模型"
              aria-label="选择模型"
            >
              <template #append>
                <button
                  type="button"
                  class="center-row h-9 shrink-0 gap-1.5 whitespace-nowrap rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface)] px-[8px] text-sm text-[var(--color-text)] outline-none transition-colors hover:border-[var(--color-border-strong)] hover:bg-[var(--color-surface-hover)] focus-visible:border-[var(--color-primary)] disabled:cursor-not-allowed disabled:opacity-50"
                  :disabled="modelListLoading || !canFetchModels"
                  @click="refreshModelList"
                >
                  <span>获取模型</span>
                </button>
              </template>
            </Combobox>
          </div>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.contextWindowTokens" />
              <span>上下文窗口</span>
            </span>
            <input
              v-model="contextWindowTokensInput"
              type="text"
              inputmode="numeric"
              placeholder="例如：200000（留空用默认值）"
              class="h-9 rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-primary)]"
            />
          </label>

          <label v-if="draft.type === 'openai'" class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.reasoningEffort" />
              <span>推理强度</span>
            </span>
            <Select
              v-model="draft.reasoningEffort"
              :options="reasoningEffortOptions"
            />
          </label>

          <label v-if="draft.type === 'anthropic'" class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.anthropicMaxTokens" />
              <span>最大输出 Token</span>
            </span>
            <input
              v-model="anthropicMaxTokensInput"
              type="text"
              inputmode="numeric"
              placeholder="例如：65536（留空用默认值）"
              class="h-9 rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-primary)]"
            />
          </label>

          <label v-if="draft.type === 'anthropic'" class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.anthropicThinkingEffort" />
              <span>思考强度</span>
            </span>
            <Select
              v-model="draft.anthropicThinkingEffort"
              :options="anthropicThinkingEffortOptions"
            />
          </label>

        </div>

        <div v-if="draft.type === 'openai'" class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.maxCompletionTokens" />
              <span>最大输出 Token</span>
            </span>
            <input
              v-model="maxCompletionTokensInput"
              type="text"
              inputmode="numeric"
              placeholder="例如：65536（留空用默认值）"
              class="h-9 rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-primary)]"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.openAIEndpoint" />
              <span>接口端点</span>
            </span>
            <Select
              v-model="draft.openAIEndpoint"
              :options="openAIEndpointOptions"
            />
          </label>
        </div>

        <div v-if="draft.type === 'openai'" class="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] p-3">
          <div class="flex items-center justify-between gap-3">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.openAIExtraParams" />
              <span>额外参数 JSON</span>
            </span>
            <label class="center-row gap-2 text-xs text-[var(--color-text)]">
              <input
                v-model="draft.openAIExtraParamsEnabled"
                type="checkbox"
                class="size-4 accent-[var(--color-primary)]"
              />
              <span>启用</span>
            </label>
          </div>
          <textarea
            v-if="draft.openAIExtraParamsEnabled"
            v-model="draft.openAIExtraParamsJSON"
            rows="5"
            spellcheck="false"
            class="mt-3 min-h-[120px] w-full resize-none rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 py-2 font-mono text-xs text-[var(--color-text)] outline-none focus:border-[var(--color-primary)]"
          />
        </div>

        <div v-if="draft.type === 'anthropic'" class="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] p-3">
          <div class="flex items-center justify-between gap-3">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.anthropicExtraParams" />
              <span>Anthropic 额外参数 JSON</span>
            </span>
            <label class="center-row gap-2 text-xs text-[var(--color-text)]">
              <input
                v-model="draft.anthropicExtraParamsEnabled"
                type="checkbox"
                class="size-4 accent-[var(--color-primary)]"
              />
              <span>启用</span>
            </label>
          </div>
          <textarea
            v-if="draft.anthropicExtraParamsEnabled"
            v-model="draft.anthropicExtraParamsJSON"
            rows="5"
            spellcheck="false"
            class="mt-3 min-h-[120px] w-full resize-none rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 py-2 font-mono text-xs text-[var(--color-text)] outline-none focus:border-[var(--color-primary)]"
          />
        </div>

        <div class="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] p-3">
          <div class="flex items-center justify-between gap-3">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.customHeaders" />
              <span>自定义请求头 JSON</span>
            </span>
            <label class="center-row gap-2 text-xs text-[var(--color-text)]">
              <input
                v-model="draft.customHeadersEnabled"
                type="checkbox"
                class="size-4 accent-[var(--color-primary)]"
              />
              <span>启用</span>
            </label>
          </div>
          <textarea
            v-if="draft.customHeadersEnabled"
            v-model="draft.customHeadersJSON"
            rows="5"
            spellcheck="false"
            class="mt-3 min-h-[120px] w-full resize-none rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 py-2 font-mono text-xs text-[var(--color-text)] outline-none focus:border-[var(--color-primary)]"
          />
        </div>

        <div class="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] p-3">
          <div class="flex items-center justify-between gap-3">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.providerFallback" />
              <span>渠道 Fallback</span>
            </span>
            <label class="center-row gap-2 text-xs text-[var(--color-text)]">
              <input
                v-model="draft.providerFallback.enabled"
                type="checkbox"
                class="size-4 accent-[var(--color-primary)]"
              />
              <span>启用</span>
            </label>
          </div>
          <div v-if="draft.providerFallback.enabled" class="mt-3 flex flex-col gap-3">
            <p class="text-xs text-[var(--color-text-secondary)]">
              启用后，此模型路由的请求将按顺序使用主渠道和备选渠道，不再使用当前渠道本身。所有渠道共享总 attempt 预算；仅在零输出时允许切换。
            </p>
            <div
              v-if="fallbackNoChannels"
              class="rounded-[6px] border border-[var(--color-warning-border)] bg-[var(--color-warning-bg)] px-3 py-2 text-xs text-[var(--color-warning-text)]"
            >
              当前没有其他已保存的渠道，请先新增并保存其他模型配置后再配置 Fallback。
            </div>
            <div
              v-if="isCrossProviderFallback"
              class="rounded-[6px] border border-[var(--color-warning-border)] bg-[var(--color-warning-bg)] px-3 py-2 text-xs text-[var(--color-warning-text)]"
            >
              检测到跨 Provider 配置（OpenAI / Anthropic 混用）。请确认工具 schema、模型语义与上下文格式兼容，不兼容时该渠道将被跳过而非降级。
            </div>
            <label v-if="!fallbackNoChannels" class="flex flex-col gap-1">
              <span class="text-xs text-[var(--color-text-secondary)]">主渠道（第 1 优先）</span>
              <Select
                v-model="draft.providerFallback.primaryChannelID"
                :options="fallbackPrimaryOptions"
              />
            </label>
            <label v-if="!fallbackNoChannels && draft.providerFallback.primaryChannelID" class="flex flex-col gap-1">
              <span class="text-xs text-[var(--color-text-secondary)]">备选渠道 1（第 2 优先，必填）</span>
              <Select
                v-model="candidate1"
                :options="fallbackCandidate1Options"
              />
            </label>
            <label v-if="!fallbackNoChannels && candidate1" class="flex flex-col gap-1">
              <span class="text-xs text-[var(--color-text-secondary)]">备选渠道 2（第 3 优先，可选）</span>
              <Select
                v-model="candidate2"
                :options="fallbackCandidate2Options"
              />
            </label>
          </div>
        </div>

        <label class="flex flex-col gap-1">
          <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
            <Tooltip :content="fieldTips.tooltipData" />
            <span>备注</span>
          </span>
          <textarea
            v-model="draft.tooltipData"
            rows="3"
            placeholder="例如：用于日常代码补全与问答"
            class="min-h-[96px] resize-none rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 py-2 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-primary)]"
          />
        </label>

      </div>
    </div>
    <div class="flex shrink-0 items-center justify-end gap-2 px-4 py-3">
      <Button variant="default" :disabled="appState.configSaving" @click="handleCancel">取消</Button>
      <Button variant="default" :disabled="isCurrentConfigTesting || appState.configSaving" @click="handleTest">
        {{ isCurrentConfigTesting ? "测试中..." : "保存并测试" }}
      </Button>
      <Button variant="primary" :disabled="appState.configSaving" @click="handleSave">
        {{ appState.configSaving ? "保存中..." : "保存" }}
      </Button>
    </div>
  </div>
</template>
