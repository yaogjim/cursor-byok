import { message } from "@/composables/useMessage";

export const showActionError = (title, detail) => {
  const normalizedDetail = String(detail || "服务错误").trim() || "服务错误";
  return message(`${title}：${normalizedDetail}`, { tone: "error" });
};
