# cursor-byok v1.80.0

本版本交付三项正确性修复：导入历史压缩丢失后续用户消息、MCP 非法参数被静默吞掉、Gateway 停止/切换超时后状态失真。三项均按已批准的最小合同实施，不改变历史持久格式、预算约束与合法参数兼容行为。

## 导入历史压缩保留全部用户片段

- 导入历史所有条目可共用 TurnSeq=0，原候选构造仅保留第一条用户消息，后续用户约束在进入摘要模型前已丢失。现按遍历位置将后续非空用户片段写入既有 Steps（`user=...`），保留重复实例及前置 assistant/已有摘要顺序。
- 不改变历史编号、持久格式、片段截断和预算；导入→HTTP 溢出恢复→摘要请求/本地 fallback→正式追加提交→第二次 run 续接的测试覆盖短历史。

## MCP 非法参数显式失败并可纠正

- 公共解析器 `DecodeMCPToolPayload` 与 direct MCP 重写原先分别吞掉 JSON/类型错误。现公共解析明确返回字段类别错误；direct MCP 解析失败走既有预派发失败收口（复用 `completePreDispatchToolError`），保留原 invocation/CallID。
- 两入口均无 MCP 执行消息、无新增 pending、一次失败工具结果/完成事件；模型在下一请求中实际收到失败结果，纠正为合法参数后恰好派发一次。
- 错误文本只含固定类别、不含输入标记；空值、对象、字符串化对象和 args 优先级合同保持不变（direct 不额外支持字符串化对象）。

## Gateway 生命周期加固

- 每个 HTTP server 实例持有独立请求取消源；正常 drain 后释放，超时后 cancel+Close 强制收尾在途连接。
- 监听切换一旦旧实例 Shutdown 超时，旧/候选 listener 均关闭并呈现停止态，可显式重启；成功/重复停止清除陈旧错误，旧 Serve 异常迟到不取消或清空新实例。
- client 在 Start/Stop 返回错误前发布最终状态；bind 失败保留旧服务可用，缺 token 等启动失败记录对应错误。

## 明确限制

- 真实 Cursor 实机、外部模型/MCP 服务与跨平台实机未授权验收；本地证据为合成数据、模拟 HTTP 上游与随机回环场景。
- 压缩修复消除结构性丢消息，不承诺有限摘要逐字保存任意长历史。
- updater 安装机制仍单独待设计，本版本未改动。

## 验证边界

- 受影响包全量与定向 `go test`（含 `-race`）、`go vet`、gofmt 与 `git diff --check` 通过；关键修复先以旧实现复现失败（RED）再验证修复。
- 早期部分测试隐式读取系统代理描述（`scutil --proxy`，只读），最终验证已用 PATH 替身隔离；详细证据见 `docs/process.md`。
- macOS 宿主平台 `task build` 成功；Windows/Linux 由远端 CI 构建各平台安装包。