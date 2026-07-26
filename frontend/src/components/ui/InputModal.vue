<script setup>
import Button from "@/components/ui/Button.vue";

const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: "提示" },
  content: { type: String, default: "" },
  placeholder: { type: String, default: "" },
  modelValue: { type: String, default: "" },
});

const emit = defineEmits(["update:visible", "update:modelValue", "confirm", "cancel"]);

function handleConfirm() {
  emit("confirm");
  emit("update:visible", false);
}

function handleCancel() {
  emit("cancel");
  emit("update:visible", false);
}

function onMaskClick() {
  handleCancel();
}

function onInput(event) {
  emit("update:modelValue", event?.target?.value ?? "");
}

function onEnter(event) {
  event.preventDefault();
  handleConfirm();
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-mask">
      <div
        v-show="visible"
        class="modal-mask-layer fixed inset-0 z-999 flex items-center justify-center bg-black/50 p-4"
        @click.self="onMaskClick"
      >
        <Transition name="modal-content">
          <div
            v-show="visible"
            class="relative z-10 w-full max-w-[380px] overflow-hidden rounded-[8px] border border-[var(--color-border)] bg-[var(--color-surface)] shadow-[var(--shadow-popover)]"
            @click.stop
          >
            <div class="rounded-[7px] p-5">
              <h3 class="mb-3 text-base font-medium text-[var(--color-text)]">
                {{ title }}
              </h3>
              <p class="mb-3 text-sm leading-relaxed text-[var(--color-text-secondary)]">
                {{ content }}
              </p>
              <input
                :value="modelValue"
                :placeholder="placeholder"
                type="text"
                class="mb-5 h-9 w-full rounded-[6px] border border-[var(--color-border-strong)] bg-[var(--color-surface)] px-3 text-sm text-[var(--color-text)] outline-none placeholder:text-[var(--color-text-muted)] focus:border-[var(--color-primary)]"
                @input="onInput"
                @keydown.enter="onEnter"
              />
              <div class="flex justify-end gap-2">
                <Button variant="default" @click="handleCancel">取消</Button>
                <Button variant="primary" @click="handleConfirm">确定</Button>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.modal-mask-enter-active,
.modal-mask-leave-active {
  transition: opacity 0.25s ease, backdrop-filter 0.25s ease;
}
.modal-mask-enter-from,
.modal-mask-leave-to {
  opacity: 0;
  backdrop-filter: blur(0);
}

.modal-content-enter-active,
.modal-content-leave-active {
  transition: all 0.25s cubic-bezier(0.34, 1.56, 0.64, 1);
}
.modal-content-enter-from,
.modal-content-leave-to {
  opacity: 0;
  transform: scale(0.9) translateY(-10px);
}
</style>
