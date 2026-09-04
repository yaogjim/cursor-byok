<script setup>
import Button from "@/components/ui/Button.vue";
import Combobox from "@/components/ui/Combobox.vue";
import Input from "@/components/ui/Input.vue";
import ModelAdapterTestCard from "@/components/ModelAdapterTestCard.vue";
import Select from "@/components/ui/Select.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { useMessage } from "@/composables/useMessage";
import {
  appState,
  buildModelAdapterTestRequestHash,
  createEmptyModelAdapter,
  applyModelAdapterTypeChange,
  applyOpenAIImageGenerationConstraint,
  CUSTOM_HEADERS_DEFAULT_JSON,
  EXTRA_PARAMS_DEFAULT_JSON,
  fetchAvailableModelIDs,
  getModelAdapterTestResult,
  getModelAdapterTestResultByID,
  isManagedCredentialSource,
  isModelAdapterTestResultStale,
  isOpenAIImageGenerationCompatible,
  normalizeModelAdapter,
  OPENAI_ENDPOINT_CHAT_COMPLETIONS,
  OPENAI_ENDPOINT_CUSTOM,
  OPENAI_ENDPOINT_RESPONSES,
  OPENAI_EXTRA_PARAMS_DEFAULT_JSON,
  runModelAdapterTest,
  saveModelAdapterAt,
  setModelsEditorDraftDirty,
  toUserError,
  validateModelAdapters,
} from "@/state/appState";
import {
  DEFAULT_PROVIDER_FALLBACK,
  formatFallbackBudgetInput,
  isFallbackChannelCompatible,
  isLogicalRoutingAdapter,
  LOGICAL_ROUTING_RUNTIME_VERIFY_HINT,
  MAX_PROVIDER_FALLBACK_CANDIDATES,
  maxConcurrentRequestsFieldError,
  parseFallbackBudgetInput,
  providerFallbackBudgetFieldError,
  shouldTestModelAdapterEndpoint,
} from "@/state/configProjection";
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

const credentialSourceOptions = [
  { label: "静态 API key", value: "static" },
  { label: "ChatGPT / Codex 订阅", value: "codex" },
  { label: "Grok 订阅", value: "grok" },
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
const editorBaseline = ref(JSON.stringify(normalizeModelAdapter(draft)));
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
const canFetchModels = computed(() => {
  const source = String(draft.credentialSource || "").trim().toLowerCase();
  const hasType = Boolean(draft.type);
  const hasBaseURL = Boolean(String(draft.baseURL || "").trim());
  const hasKey = Boolean(String(draft.apiKey || "").trim());
  if (!hasType) {
    return false;
  }
  if (source === "codex") {
    return true;
  }
  return hasBaseURL && (isManagedCredentialSource(draft.credentialSource) || hasKey);
});
const isManagedCredential = computed(() => isManagedCredentialSource(draft.credentialSource));
const canEnableOpenAIImageGeneration = computed(() => isOpenAIImageGenerationCompatible(draft));
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

const fieldTips = {
  displayName: "仅用于界面展示，便于你区分不同模型。",
  modelID: "可以直接输入模型标识，或从服务端返回的列表中选择。",
  baseURL: "模型服务的 API 根地址。选择 Codex 或 Grok 订阅后，系统会固定使用对应的官方上游地址。",
  apiKey: "调用该模型服务需要使用的访问密钥。订阅认证渠道请前往接入中心对应的 Codex 或 Grok 页面管理，不要把 token 填到这里。",
  credentialSource: "静态 API key 使用本页密钥；Codex / Grok 订阅从本机认证副本解析，不会写入 config.yaml。",
  contextWindowTokens: "模型单次可接受的最大上下文 Token 数。留空时使用默认值。",
  reasoningEffort: "仅当模型支持 reasoning_effort 时才选择推理强度；选择“不设置”后，请求不会携带该参数。越高通常越稳，但也可能更慢。",
  maxCompletionTokens: "单次回复允许生成的最大 Token 数。留空时使用默认值。",
  openAIEndpoint: "选择接口协议端点。选“自定义路径”时，请在接口地址栏填写完整请求地址（含 /chat/completions 或 /responses 路径后缀），系统会根据末段自动判断协议形态。",
  openAIExtraParams: "开启后会把 JSON 对象覆盖到 OpenAI 请求体。同名字段以这里为准。OpenAI service_tier 支持 auto、default、flex、scale、priority。",
  openAIImageGeneration: "仅兼容的上游应开启。仅 OpenAI、静态 API key、/v1/responses 端点可启用；切换类型、端点或凭据后会自动关闭。",
  customHeaders: "开启后会把 JSON 对象覆盖到最终请求头。同名请求头以这里为准，值必须是字符串。",
  anthropicExtraParams: "开启后会把 JSON 对象覆盖到 Anthropic 请求体。同名字段以这里为准。",
  anthropicMaxTokens: "Anthropic 模型单次回复允许生成的最大 Token 数。留空时使用默认值。",
  anthropicThinkingEffort: "Anthropic adaptive thinking 的思考强度。请求会固定使用新版 thinking.type=adaptive。",
  maxConcurrentRequests: "0 或不填表示不限制。同一接口地址与密钥的物理渠道共享该上限；无空闲槽时固定等待 2 秒。",
  providerFallback: "启用后，此模型成为逻辑路由身份（建议仅子代理选择）。请求按主渠道和备选渠道顺序尝试，alias 自身不会向虚拟 endpoint 发请求。5xx 等失败先在当前渠道再试一次，然后才安全切换；一旦出现任何原始字节、model event 或 side effect，禁止切换。候选仅限相同适配器类型和相同协议端点的物理渠道。",
  maxHttpAttempts: "整条 fallback 链共享的最大 HTTP 尝试次数，默认 5，允许 2–9。越界会报错而不会静默截断。",
  maxWaitSeconds: "整条 fallback 链共享的累计实际退避等待秒数，默认 8，允许 1–30。不含模型推理、首 Token、渠道执行或容量等待。越界会报错而不会静默截断。",
  maxAttemptsPerChannel: "同一渠道连续尝试次数，默认 2，允许 1–3。5xx 等失败会先在当前渠道再试一次，再安全切换。",
  connectTimeoutSeconds: "建立上游连接的超时秒数，默认 30，允许 5–120。",
  firstEventTimeoutSeconds: "等待首个模型事件的秒数，默认 600，允许 60–1800。保证长首 Token 不会被 8 秒退避预算误杀。",
  streamIdleTimeoutSeconds: "流式响应空闲超时秒数，默认 240，允许 30–900。按模型配置，不再使用全局空闲超时。",
  callTimeoutSeconds: "整次调用墙钟超时秒数，默认 7200，允许 900–21600。不是退避等待预算。",
  tooltipData: "模型列表 hover 时显示的备注说明。",
};

function currentModelIDOptions() {
  const currentModelID = String(draft.modelID || "").trim();
  return currentModelID ? [currentModelID] : [];
}

async function refreshModelList() {
  const baseURL = String(draft.baseURL || "").trim();
  const apiKey = String(draft.apiKey || "").trim();
  const credentialSource = draft.credentialSource;
  const isCodex = String(credentialSource || "").trim().toLowerCase() === "codex";
  if (!draft.type || (!isCodex && !baseURL) || !(isManagedCredentialSource(credentialSource) || apiKey)) {
    modelListRequestSeq.value += 1;
    availableModelIDs.value = currentModelIDOptions();
    modelListLoading.value = false;
    return [];
  }

  const requestSeq = modelListRequestSeq.value + 1;
  modelListRequestSeq.value = requestSeq;
  modelListLoading.value = true;
  const currentModelID = String(draft.modelID || "").trim();
  availableModelIDs.value = currentModelIDOptions();
  try {
    const models = await fetchAvailableModelIDs({
      type: draft.type,
      baseURL,
      apiKey: isManagedCredentialSource(credentialSource) ? "" : apiKey,
      credentialSource,
      customHeadersEnabled: draft.customHeadersEnabled,
      customHeadersJSON: draft.customHeadersJSON,
    });
    if (requestSeq !== modelListRequestSeq.value) {
      return availableModelIDs.value;
    }
    const merged = [];
    const seen = new Set();
    for (const modelID of [currentModelID, ...models]) {
      const id = String(modelID || "").trim();
      if (!id || seen.has(id)) {
        continue;
      }
      seen.add(id);
      merged.push(id);
    }
    availableModelIDs.value = merged;
    return models;
  } catch (_error) {
    if (requestSeq === modelListRequestSeq.value) {
      availableModelIDs.value = currentModelIDOptions();
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
  editorBaseline.value = JSON.stringify(normalizeModelAdapter(draft));
  setModelsEditorDraftDirty(false);
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
  if (isLogicalRoutingAdapter(result.adapter)) {
    message(LOGICAL_ROUTING_RUNTIME_VERIFY_HINT);
  }
  emit("saved", result.adapter);
  emit("close");
}

function handleCancel() {
  emit("close");
}

function handleModelTypeChange(type) {
  if (draft.type === type) {
    return;
  }
  applyModelAdapterTypeChange(draft, type);
  modelListRequestSeq.value += 1;
  modelListLoading.value = false;
  availableModelIDs.value = currentModelIDOptions();
}

async function handleTest() {
  localTestFailure.value = "";
  try {
    const saved = await persistDraft();
    if (!saved.ok || !saved.adapter) {
      return;
    }
    if (!shouldTestModelAdapterEndpoint(saved.adapter)) {
      message(LOGICAL_ROUTING_RUNTIME_VERIFY_HINT);
      emit("saved", saved.adapter);
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
  draft,
  () => {
    setModelsEditorDraftDirty(JSON.stringify(normalizeModelAdapter(draft)) !== editorBaseline.value);
  },
  { deep: true, immediate: true },
);

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
  () => [draft.type, draft.credentialSource, draft.openAIEndpoint],
  () => {
    applyOpenAIImageGenerationConstraint(draft);
  },
);

watch(
  () => [draft.type, draft.baseURL, draft.apiKey, draft.credentialSource, draft.customHeadersEnabled, draft.customHeadersJSON],
  () => {
    window.clearTimeout(modelListDebounceTimer);
    const baseURL = String(draft.baseURL || "").trim();
    const apiKey = String(draft.apiKey || "").trim();
    const isCodex = String(draft.credentialSource || "").trim().toLowerCase() === "codex";
    if ((!isCodex && !baseURL) || !(isManagedCredentialSource(draft.credentialSource) || apiKey)) {
      modelListRequestSeq.value += 1;
      modelListLoading.value = false;
      availableModelIDs.value = currentModelIDOptions();
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
  setModelsEditorDraftDirty(false);
});

// ── providerFallback UI 支持 ──

// 其他已保存渠道（排除当前编辑中的适配器）；仅同类型且同协议端点的物理渠道可入链
const otherAdapters = computed(() =>
  appState.modelAdapters.filter(
    (a) => a.id && a.id !== draft.id && !isLogicalRoutingAdapter(a) && isFallbackChannelCompatible(draft, a),
  ),
);

function fallbackChannelOptionLabel(adapter, priority) {
  const name = adapter.displayName || adapter.id;
  const provider = adapter.type === "anthropic" ? "Anthropic" : "OpenAI";
  const modelID = adapter.modelID || "—";
  if (priority) {
    return `${name} · ${provider} · ${modelID} · 第 ${priority} 优先`;
  }
  return `${name} · ${provider} · ${modelID}`;
}

// 渠道下拉基础选项，含空"不选择"项
const fallbackChannelBaseOptions = computed(() => [
  { label: "── 不选择 ──", value: "", icon: "icon-[mdi--minus-circle-outline]" },
  ...otherAdapters.value.map((a) => ({
    label: fallbackChannelOptionLabel(a),
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

const fallbackNoChannels = computed(() =>
  draft.providerFallback.enabled && otherAdapters.value.length === 0,
);

const fallbackCandidateSlotIndexes = computed(() => {
  if (fallbackNoChannels.value || !draft.providerFallback.primaryChannelID) {
    return [];
  }
  const ids = draft.providerFallback.candidateChannelIDs || [];
  const slots = [];
  for (let index = 0; index < MAX_PROVIDER_FALLBACK_CANDIDATES; index += 1) {
    if (index > 0 && !ids[index - 1]) {
      break;
    }
    slots.push(index);
  }
  return slots;
});

function candidateSlotValue(index) {
  return draft.providerFallback.candidateChannelIDs[index] ?? "";
}

function candidateSlotOptions(index) {
  const ids = draft.providerFallback.candidateChannelIDs || [];
  const taken = new Set(
    [draft.providerFallback.primaryChannelID, ...ids.filter((_, slotIndex) => slotIndex !== index)].filter(Boolean),
  );
  return fallbackChannelBaseOptions.value.filter((option) => !option.value || !taken.has(option.value));
}

function candidateSlotLabel(index) {
  const required = index === 0 ? "必填" : "可选";
  return `备选渠道 ${index + 1}（第 ${index + 2} 优先，${required}）`;
}

function setCandidateSlot(index, val) {
  const value = String(val || "").trim();
  const current = Array.isArray(draft.providerFallback.candidateChannelIDs)
    ? draft.providerFallback.candidateChannelIDs.slice()
    : [];
  if (!value) {
    draft.providerFallback.candidateChannelIDs = current.slice(0, index);
    return;
  }
  const next = current.slice();
  next[index] = value;
  draft.providerFallback.candidateChannelIDs = next;
}

function createFallbackBudgetModel(key) {
  return computed({
    get() {
      return formatFallbackBudgetInput(draft.providerFallback[key]);
    },
    set(value) {
      draft.providerFallback[key] = parseFallbackBudgetInput(value);
    },
  });
}

const maxHttpAttemptsInput = createFallbackBudgetModel("maxHttpAttempts");
const maxWaitSecondsInput = createFallbackBudgetModel("maxWaitSeconds");
const maxAttemptsPerChannelInput = createFallbackBudgetModel("maxAttemptsPerChannel");
const connectTimeoutSecondsInput = createFallbackBudgetModel("connectTimeoutSeconds");
const firstEventTimeoutSecondsInput = createFallbackBudgetModel("firstEventTimeoutSeconds");
const streamIdleTimeoutSecondsInput = createFallbackBudgetModel("streamIdleTimeoutSeconds");
const callTimeoutSecondsInput = createFallbackBudgetModel("callTimeoutSeconds");
const maxHttpAttemptsError = computed(() =>
  providerFallbackBudgetFieldError("maxHttpAttempts", draft.providerFallback.maxHttpAttempts),
);
const maxWaitSecondsError = computed(() =>
  providerFallbackBudgetFieldError("maxWaitSeconds", draft.providerFallback.maxWaitSeconds),
);
const maxAttemptsPerChannelError = computed(() =>
  providerFallbackBudgetFieldError("maxAttemptsPerChannel", draft.providerFallback.maxAttemptsPerChannel),
);
const connectTimeoutSecondsError = computed(() =>
  providerFallbackBudgetFieldError("connectTimeoutSeconds", draft.providerFallback.connectTimeoutSeconds),
);
const firstEventTimeoutSecondsError = computed(() =>
  providerFallbackBudgetFieldError("firstEventTimeoutSeconds", draft.providerFallback.firstEventTimeoutSeconds),
);
const streamIdleTimeoutSecondsError = computed(() =>
  providerFallbackBudgetFieldError("streamIdleTimeoutSeconds", draft.providerFallback.streamIdleTimeoutSeconds),
);
const callTimeoutSecondsError = computed(() =>
  providerFallbackBudgetFieldError("callTimeoutSeconds", draft.providerFallback.callTimeoutSeconds),
);
const isLogicalRoutingDraft = computed(() => isLogicalRoutingAdapter(draft));
const maxConcurrentRequestsInput = computed({
  get() {
    return formatFallbackBudgetInput(draft.maxConcurrentRequests);
  },
  set(value) {
    draft.maxConcurrentRequests = parseFallbackBudgetInput(value);
  },
});
const maxConcurrentRequestsError = computed(() =>
  maxConcurrentRequestsFieldError(draft.maxConcurrentRequests),
);

watch(
  () => draft.providerFallback.enabled,
  (enabled) => {
    if (enabled) {
      draft.maxConcurrentRequests = 0;
    }
  },
);
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
              :disabled="isManagedCredential"
              class="h-9 rounded-[6px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none focus:border-[var(--color-primary)] disabled:cursor-not-allowed disabled:opacity-70"
            />
          </label>

          <label class="flex flex-col gap-1">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.credentialSource" />
              <span>凭据来源</span>
            </span>
            <Select
              v-model="draft.credentialSource"
              :options="credentialSourceOptions"
              aria-label="凭据来源"
            />
          </label>

          <label v-if="!isManagedCredential" class="flex flex-col gap-1">
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
          <p v-else class="note plain md:col-span-2">
            该渠道使用接入中心对应的 Codex 或 Grok 授权，并固定使用对应官方上游地址和协议端点；token 不会写入 config.yaml。
          </p>

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
              :disabled="isManagedCredential"
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

        <div v-if="draft.type === 'openai'" class="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] p-3">
          <label class="flex items-center justify-between gap-3 text-sm text-[var(--color-text)]">
            <span class="center-row justify-start gap-1.5">
              <Tooltip :content="fieldTips.openAIImageGeneration" />
              <span>OpenAI Responses 原生媒体生成</span>
            </span>
            <input
              v-model="draft.openAIImageGenerationEnabled"
              type="checkbox"
              class="size-4 accent-[var(--color-primary)] disabled:cursor-not-allowed disabled:opacity-50"
              :disabled="!canEnableOpenAIImageGeneration"
            />
          </label>
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

        <label v-if="!isLogicalRoutingDraft" class="flex flex-col gap-1">
          <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
            <Tooltip :content="fieldTips.maxConcurrentRequests" />
            <span>上游并发上限</span>
          </span>
          <input
            v-model="maxConcurrentRequestsInput"
            type="text"
            inputmode="numeric"
            placeholder="例如：2（留空表示不限制）"
            :aria-invalid="Boolean(maxConcurrentRequestsError)"
            class="h-9 rounded-[6px] border bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none"
            :class="maxConcurrentRequestsError
              ? 'border-[var(--color-error-border)] focus:border-[var(--color-error-text)]'
              : 'border-[var(--color-border)] focus:border-[var(--color-primary)]'"
          />
          <span v-if="maxConcurrentRequestsError" class="text-[11px] text-[var(--color-error-text)]">{{ maxConcurrentRequestsError }}</span>
          <span v-else class="text-[11px] text-[var(--color-text-muted)]">0 或不填表示不限制；同一接口与密钥共享，等待固定 2 秒。</span>
        </label>

        <div class="rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface-muted)] p-3">
          <div class="flex items-center justify-between gap-3">
            <span class="center-row justify-start gap-1.5 text-sm text-[var(--color-text)]">
              <Tooltip :content="fieldTips.providerFallback" />
              <span>渠道 Fallback</span>
              <span
                v-if="isLogicalRoutingDraft"
                class="rounded-[999px] border border-[var(--color-warning-border)] bg-[var(--color-warning-bg)] px-[7px] py-[2px] text-[11px] font-medium text-[var(--color-warning-text)]"
              >
                逻辑路由（建议仅子代理）
              </span>
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
              启用后，此模型是逻辑路由 alias（建议仅子代理选择），自身不会向虚拟 endpoint 发请求。5xx 等失败先在当前渠道再试一次，然后才安全切换；任何原始字节、event 或 side effect 后不再切换。首事件超时默认 600 秒，保证长首 Token 不会被 8 秒退避预算误杀。候选仅限相同类型和相同协议端点的物理渠道。
            </p>
            <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
              <label class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-xs text-[var(--color-text-secondary)]">
                  <Tooltip :content="fieldTips.maxHttpAttempts" />
                  <span>全链最大 HTTP 尝试次数（默认 5）</span>
                </span>
                <input
                  v-model="maxHttpAttemptsInput"
                  type="text"
                  inputmode="numeric"
                  :placeholder="String(DEFAULT_PROVIDER_FALLBACK.maxHttpAttempts)"
                  :aria-invalid="Boolean(maxHttpAttemptsError)"
                  class="h-9 rounded-[6px] border bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none"
                  :class="maxHttpAttemptsError
                    ? 'border-[var(--color-error-border)] focus:border-[var(--color-error-text)]'
                    : 'border-[var(--color-border)] focus:border-[var(--color-primary)]'"
                />
                <span v-if="maxHttpAttemptsError" class="text-[11px] text-[var(--color-error-text)]">{{ maxHttpAttemptsError }}</span>
                <span v-else class="text-[11px] text-[var(--color-text-muted)]">允许 2–9；越界报错，不会静默截断。</span>
              </label>
              <label class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-xs text-[var(--color-text-secondary)]">
                  <Tooltip :content="fieldTips.maxWaitSeconds" />
                  <span>全链最大退避等待秒数（默认 8）</span>
                </span>
                <input
                  v-model="maxWaitSecondsInput"
                  type="text"
                  inputmode="numeric"
                  :placeholder="String(DEFAULT_PROVIDER_FALLBACK.maxWaitSeconds)"
                  :aria-invalid="Boolean(maxWaitSecondsError)"
                  class="h-9 rounded-[6px] border bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none"
                  :class="maxWaitSecondsError
                    ? 'border-[var(--color-error-border)] focus:border-[var(--color-error-text)]'
                    : 'border-[var(--color-border)] focus:border-[var(--color-primary)]'"
                />
                <span v-if="maxWaitSecondsError" class="text-[11px] text-[var(--color-error-text)]">{{ maxWaitSecondsError }}</span>
                <span v-else class="text-[11px] text-[var(--color-text-muted)]">仅累计实际退避，不含推理/首 Token/渠道执行/容量等待；允许 1–30。</span>
              </label>
              <label class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-xs text-[var(--color-text-secondary)]">
                  <Tooltip :content="fieldTips.maxAttemptsPerChannel" />
                  <span>单渠道最大尝试次数（默认 2）</span>
                </span>
                <input
                  v-model="maxAttemptsPerChannelInput"
                  type="text"
                  inputmode="numeric"
                  :placeholder="String(DEFAULT_PROVIDER_FALLBACK.maxAttemptsPerChannel)"
                  :aria-invalid="Boolean(maxAttemptsPerChannelError)"
                  class="h-9 rounded-[6px] border bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none"
                  :class="maxAttemptsPerChannelError
                    ? 'border-[var(--color-error-border)] focus:border-[var(--color-error-text)]'
                    : 'border-[var(--color-border)] focus:border-[var(--color-primary)]'"
                />
                <span v-if="maxAttemptsPerChannelError" class="text-[11px] text-[var(--color-error-text)]">{{ maxAttemptsPerChannelError }}</span>
                <span v-else class="text-[11px] text-[var(--color-text-muted)]">允许 1–3；5xx 等先当前渠道再试一次再切换。</span>
              </label>
              <label class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-xs text-[var(--color-text-secondary)]">
                  <Tooltip :content="fieldTips.connectTimeoutSeconds" />
                  <span>建连超时秒数（默认 30）</span>
                </span>
                <input
                  v-model="connectTimeoutSecondsInput"
                  type="text"
                  inputmode="numeric"
                  :placeholder="String(DEFAULT_PROVIDER_FALLBACK.connectTimeoutSeconds)"
                  :aria-invalid="Boolean(connectTimeoutSecondsError)"
                  class="h-9 rounded-[6px] border bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none"
                  :class="connectTimeoutSecondsError
                    ? 'border-[var(--color-error-border)] focus:border-[var(--color-error-text)]'
                    : 'border-[var(--color-border)] focus:border-[var(--color-primary)]'"
                />
                <span v-if="connectTimeoutSecondsError" class="text-[11px] text-[var(--color-error-text)]">{{ connectTimeoutSecondsError }}</span>
                <span v-else class="text-[11px] text-[var(--color-text-muted)]">允许 5–120。</span>
              </label>
              <label class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-xs text-[var(--color-text-secondary)]">
                  <Tooltip :content="fieldTips.firstEventTimeoutSeconds" />
                  <span>首事件超时秒数（默认 600）</span>
                </span>
                <input
                  v-model="firstEventTimeoutSecondsInput"
                  type="text"
                  inputmode="numeric"
                  :placeholder="String(DEFAULT_PROVIDER_FALLBACK.firstEventTimeoutSeconds)"
                  :aria-invalid="Boolean(firstEventTimeoutSecondsError)"
                  class="h-9 rounded-[6px] border bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none"
                  :class="firstEventTimeoutSecondsError
                    ? 'border-[var(--color-error-border)] focus:border-[var(--color-error-text)]'
                    : 'border-[var(--color-border)] focus:border-[var(--color-primary)]'"
                />
                <span v-if="firstEventTimeoutSecondsError" class="text-[11px] text-[var(--color-error-text)]">{{ firstEventTimeoutSecondsError }}</span>
                <span v-else class="text-[11px] text-[var(--color-text-muted)]">允许 60–1800；保证长首 Token 不是 8 秒 wait budget。</span>
              </label>
              <label class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-xs text-[var(--color-text-secondary)]">
                  <Tooltip :content="fieldTips.streamIdleTimeoutSeconds" />
                  <span>流空闲超时秒数（默认 240）</span>
                </span>
                <input
                  v-model="streamIdleTimeoutSecondsInput"
                  type="text"
                  inputmode="numeric"
                  :placeholder="String(DEFAULT_PROVIDER_FALLBACK.streamIdleTimeoutSeconds)"
                  :aria-invalid="Boolean(streamIdleTimeoutSecondsError)"
                  class="h-9 rounded-[6px] border bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none"
                  :class="streamIdleTimeoutSecondsError
                    ? 'border-[var(--color-error-border)] focus:border-[var(--color-error-text)]'
                    : 'border-[var(--color-border)] focus:border-[var(--color-primary)]'"
                />
                <span v-if="streamIdleTimeoutSecondsError" class="text-[11px] text-[var(--color-error-text)]">{{ streamIdleTimeoutSecondsError }}</span>
                <span v-else class="text-[11px] text-[var(--color-text-muted)]">允许 30–900。</span>
              </label>
              <label class="flex flex-col gap-1">
                <span class="center-row justify-start gap-1.5 text-xs text-[var(--color-text-secondary)]">
                  <Tooltip :content="fieldTips.callTimeoutSeconds" />
                  <span>整次调用超时秒数（默认 7200）</span>
                </span>
                <input
                  v-model="callTimeoutSecondsInput"
                  type="text"
                  inputmode="numeric"
                  :placeholder="String(DEFAULT_PROVIDER_FALLBACK.callTimeoutSeconds)"
                  :aria-invalid="Boolean(callTimeoutSecondsError)"
                  class="h-9 rounded-[6px] border bg-[var(--color-surface-muted)] px-3 text-sm text-[var(--color-text)] outline-none"
                  :class="callTimeoutSecondsError
                    ? 'border-[var(--color-error-border)] focus:border-[var(--color-error-text)]'
                    : 'border-[var(--color-border)] focus:border-[var(--color-primary)]'"
                />
                <span v-if="callTimeoutSecondsError" class="text-[11px] text-[var(--color-error-text)]">{{ callTimeoutSecondsError }}</span>
                <span v-else class="text-[11px] text-[var(--color-text-muted)]">允许 900–21600；墙钟超时，不是退避预算。</span>
              </label>
            </div>
            <div
              v-if="fallbackNoChannels"
              class="rounded-[6px] border border-[var(--color-warning-border)] bg-[var(--color-warning-bg)] px-3 py-2 text-xs text-[var(--color-warning-text)]"
            >
              当前没有其他已保存的同类型同协议渠道，请先新增并保存兼容的物理渠道后再配置 Fallback。
            </div>
            <label v-if="!fallbackNoChannels" class="flex flex-col gap-1">
              <span class="text-xs text-[var(--color-text-secondary)]">主渠道（第 1 优先）</span>
              <Select
                v-model="draft.providerFallback.primaryChannelID"
                :options="fallbackPrimaryOptions"
              />
            </label>
            <label
              v-for="slotIndex in fallbackCandidateSlotIndexes"
              :key="slotIndex"
              class="flex flex-col gap-1"
            >
              <span class="text-xs text-[var(--color-text-secondary)]">{{ candidateSlotLabel(slotIndex) }}</span>
              <Select
                :model-value="candidateSlotValue(slotIndex)"
                :options="candidateSlotOptions(slotIndex)"
                @update:model-value="setCandidateSlot(slotIndex, $event)"
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
