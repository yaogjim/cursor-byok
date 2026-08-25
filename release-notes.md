# Cursor助手 v0.0.49.6

本版本完善多层模型路由与配置写入事务，并增加默认关闭的物理上游容量保护能力；同时修复 Provider fallback 在共享预算下过早耗尽、后续渠道没有执行机会的问题，并扩展 fallback 配置与测试交互。

- Provider fallback 采用“保证渠道覆盖，再用剩余预算重试”：先为后续兼容渠道各预留 1 次 HTTP attempt，再把剩余预算用于当前渠道重试；默认 3 渠道/5 attempts 为 `3+1+1`，5 渠道/5 attempts 为 `1+1+1+1+1`，5 渠道/9 attempts 为 `3+3+1+1+1`。
- 逻辑模型可通过 Provider fallback 在无输出、无副作用窗口内按配置顺序切换；全链 HTTP 尝试默认 5 次（可配置 2–9），退避等待默认 8 秒（可配置 1–30），单渠道最多 3 次。
- fallback 链最多支持 1 个主渠道和 4 个有序物理候选渠道；前后端保持引用、兼容性、排重和预算校验，预算不足时按配置顺序覆盖到预算耗尽。
- “测试全部”静默跳过逻辑 alias，单独测试逻辑 alias 仍提示通过实际运行验证；修复长下拉列表的真实滚动容器和键盘访问。
- 普通配置保存会保留最新 `lastAgentModelHash`；运行时 hash 更新只 patch 该字段，相同值不写盘、不通知配置 listener；完整导入仍是显式全量替换。
- 物理 adapter 可设置 `maxConcurrentRequests`（1–16）；缺失或 0 保持不限流。同 provider、规范化 Base URL 与 API Key 的渠道共享进程内容量槽，并必须配置相同上限；逻辑 fallback alias 必须为 0。
- 容量槽覆盖一次物理渠道的完整 Stream 和同渠道重试，最多等待 2 秒。容量超时返回 typed `capacity_unavailable`，不消耗 HTTP attempt 或 fallback 退避预算，只在零输出安全窗口切换到不同上游组。
- 独立工具 `tools/cursor-cli-model-pool` 可按优先级启动物理模型，禁止引用启用 fallback 的逻辑 alias；它保持独立 module，不进入客户端发布包。
- 已知边界：当前真实 `grok-HA` 未启用容量限制；CLI 两模型故障切换和 Wails 浏览器视觉验收仍是独立证据缺口，不作为本版本已完成能力。
