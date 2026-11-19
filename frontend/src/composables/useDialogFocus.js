import { nextTick, onBeforeUnmount, watch } from "vue";

// 同一时刻仅最上层已打开的弹窗处理键盘事件。
const dialogs = [];
const focusableSelector = "button:not([disabled]), a[href], input:not([disabled]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex='-1'])";

export function useDialogFocus({ open, panelRef, initialFocus, onEscape }) {
  const token = {};
  let previous = null;
  let active = false;
  function handleKeydown(event) {
    if (dialogs[dialogs.length - 1] !== token || !open()) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopImmediatePropagation();
      onEscape();
      return;
    }
    if (event.key !== "Tab" || !panelRef.value) return;
    const elements = [...panelRef.value.querySelectorAll(focusableSelector)]
      .filter((element) => element.getClientRects().length && !element.disabled && element.tabIndex >= 0);
    const first = elements[0];
    const last = elements[elements.length - 1];
    if (!first) {
      event.preventDefault();
      panelRef.value.focus();
    } else if (!panelRef.value.contains(document.activeElement)
      || (event.shiftKey && document.activeElement === first)
      || (!event.shiftKey && document.activeElement === last)) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
    }
  }
  function deactivate() {
    if (!active) return;
    const wasTop = dialogs[dialogs.length - 1] === token;
    dialogs.splice(dialogs.indexOf(token), 1);
    document.removeEventListener("keydown", handleKeydown);
    active = false;
    if (wasTop && previous instanceof HTMLElement && previous.isConnected) previous.focus();
    previous = null;
  }
  watch(open, (value) => {
    if (!value) return deactivate();
    previous = document.activeElement;
    active = true;
    dialogs.push(token);
    document.addEventListener("keydown", handleKeydown);
    nextTick(() => {
      if (active && dialogs[dialogs.length - 1] === token) (initialFocus() || panelRef.value)?.focus();
    });
  }, { immediate: true });
  onBeforeUnmount(deactivate);
}
