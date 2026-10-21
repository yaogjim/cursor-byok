<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Select from "@/components/ui/Select.vue";
import { computed, ref, watch } from "vue";

const props = defineProps({
  client: {
    type: String,
    default: "anthropic",
  },
});

const copy = {
  anthropic: {
    title: "Anthropic 接入",
    saveLabel: "保存 Anthropic 配置",
    accountHint: "支持独立套餐或重新授权现有账号",
  },
};

const modeOptions = [{ label: "深度集成", value: "deep" }];
const accessMode = ref("deep");
const panel = computed(() => copy[props.client] || copy.anthropic);

watch(
  () => props.client,
  () => {
    accessMode.value = "deep";
  },
);
</script>

<template>
  <div class="subscription-access-panel">
    <div class="subscription-access-head">
      <div class="min-w-0">
        <h2 class="subscription-access-title">{{ panel.title }}</h2>
        <p class="subscription-access-subtitle">
          管理本机配置与多个订阅号授权，账号调用时路由到各自账户
        </p>
      </div>
      <Button variant="primary" class="shrink-0" disabled>
        <span class="icon-[mdi--plus] text-[15px]" aria-hidden="true" />
        添加授权
      </Button>
    </div>

    <Card :padded="false" class="subscription-mode-card">
      <div class="subscription-mode-row">
        <span class="subscription-mode-label">接入模式</span>
        <div class="subscription-mode-select">
          <Select
            v-model="accessMode"
            :options="modeOptions"
            disabled
            aria-label="接入模式"
          />
        </div>
        <Button disabled>已同步</Button>
        <Button disabled>查看同步</Button>
      </div>
      <div class="subscription-mode-hint">
        切换接入方式不会自动同步已有授权
      </div>
    </Card>

    <Card :padded="false" class="subscription-account-card">
      <div class="subscription-account-head">
        <h3 class="subscription-account-title">订阅授权</h3>
        <p class="subscription-account-subtitle">
          <span>0 个账号</span>
          {{ panel.accountHint }}
        </p>
      </div>

      <div class="subscription-account-empty">
        <span class="subscription-account-empty-icon icon-[mdi--account-outline]" aria-hidden="true" />
        <div>
          <div class="subscription-account-empty-title">暂无授权账号</div>
          <div class="subscription-account-empty-copy">
            当前版本尚未接通此客户端的账号授权能力，不会展示截图中的演示账号。
          </div>
        </div>
      </div>

      <div class="subscription-account-footer">
        <span class="config-action-status">未配置</span>
        <div class="config-action-buttons">
          <Button disabled>测试连接</Button>
          <Button variant="primary" disabled>{{ panel.saveLabel }}</Button>
        </div>
      </div>
    </Card>
  </div>
</template>