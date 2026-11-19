import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

// 使用真实提示实现与 Vue reactive，仅替换时钟，避免等待自动关闭时长。
const scheduled = [];
globalThis.window = { setTimeout(callback, duration) { scheduled.push({ callback, duration }); return scheduled.length; } };
const source = readFileSync(new URL("../src/composables/useMessage.js", import.meta.url), "utf8")
  .replace('from "vue"', `from ${JSON.stringify(import.meta.resolve("vue"))}`);
const messageURL = `data:text/javascript;base64,${Buffer.from(source).toString("base64")}`;
const { message, messageState } = await import(messageURL);
message("中性提示");
assert.equal(messageState.current.tone, "info");
assert.equal(scheduled.at(-1).duration, 2400);
message("已完成", { tone: "success" });
assert.equal(messageState.current.tone, "success");
assert.equal(scheduled.at(-1).duration, 2400);
const scheduledBeforeError = scheduled.length;
message("失败", { tone: "error" });
assert.equal(messageState.current.tone, "error");
assert.equal(messageState.current.timer, null);
assert.equal(scheduled.length, scheduledBeforeError);
message("限时错误", { tone: "error", duration: 900 });
assert.equal(scheduled.at(-1).duration, 900);
message.remove(messageState.current.id, { force: true });
assert.equal(messageState.current, null);
message("常驻成功", { tone: "success", duration: 0 });
assert.equal(messageState.current.timer, null);
message.clear();
assert.equal(messageState.current, null);
const errorSource = readFileSync(new URL("../src/composables/useActionError.js", import.meta.url), "utf8")
  .replace('"@/composables/useMessage"', JSON.stringify(messageURL));
const { showActionError } = await import(`data:text/javascript;base64,${Buffer.from(errorSource).toString("base64")}`);
showActionError("保存失败", "   ");
assert.equal(messageState.current.content, "保存失败：服务错误");
assert.equal(messageState.current.tone, "error");
showActionError("刷新失败", "具体原因");
assert.equal(messageState.current.content, "刷新失败：具体原因");
console.log("UI feedback tests passed");
