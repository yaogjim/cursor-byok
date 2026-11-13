# cursor-byok v1.90.1

本版本修复 Cursor 3.29.15 的 `BidiAppend` 二进制载荷兼容问题：新版客户端默认把 Agent 上行消息放进 `data_binary`，旧实现只读 `data`（hex），合法请求被当成空消息拒绝。修复不改变历史持久格式、旧 hex 上行行为与既有错误语义。

`v1.90.0` 标签因 Git 托管 macOS 构建机标签 `macos-14` 停用而未产出发布包，本版本改用 `macos-15` 构建并以 `v1.90.1` 重新发布，产品代码与 `v1.90.0` 完全一致。

## 兼容 Cursor 3.29.15 的 BidiAppend 二进制载荷

- 新增统一字节解码头 `ReadAppendPayload`：`data_binary` 非空时优先使用，否则按旧 `data` hex 解码；空载荷、非法 hex 与 protobuf 解码失败沿用修改前的错误语义，不放宽失败判定。
- `BidiAppend` 处理、客户端消息分流路由与 `cursor-proxy-debugger` 共用同一解码头，避免三处各自判断编码造成行为分叉。
- 两字段同时出现时以 `data_binary` 为准（修改前路由层临时实现以 hex 为准）。3.29.15 客户端按开关二选一发送、不双发，该差异当前无实际输入触发；规则已写入系统架构 §D2.2。
- 3.29.15 之前版本、以及 3.29.15 打开 `bidi_append_binary_encoding_disabled` 时的旧 hex 上行保持修改前行为，两种编码都可正常处理。
- 诊断记录只增加载荷来源与长度（`payload_source`、`payload_bytes`），不落盘未知二进制原文。

## 明确限制

- 协议提取器尚不支持 Cursor 新版描述符格式（`static $(){return["TypeName|..."]}`），本版本未做适配，直接全量 `sync:proto` 会覆盖现有产物，因此未执行。
- 更早版本 Cursor 的实机回归未执行（本机已升级到 3.29.15），也未做跨平台实机验收；旧版本兼容结论来自与修改前实现等价的合成契约测试，不外推为实机通过。
- 控制面未登录时插件/MCP 查询返回 404 的问题属独立议题，策略尚未决定，本版本未改动。
- updater 安装机制仍未设计完成，本版本未改动。

## 验证边界

- 先行回归在原实现下复现 `unsupported client message kind`，修复后通过；覆盖二进制载荷上行、二进制载荷路由、Host 的 gzip/hex/binary 三种合约以及调试器对新旧两种编码的解码断言。
- `go test ./internal/backend ...`、`go test ./cursor-proxy-debugger`、定向 race/vet、gofmt 与 `git diff --check` 通过，详细命令与退出码见 `docs/process.md`。
- 维护窗口内替换本机应用后，用户确认普通对话正常回答问题，日志确认工具执行结果回传与模型续答；取消、同会话恢复与子任务路径按只读 debug 记录核对，整体仍为 `verified-partial`。
- 本机未执行打包编译（本次仅版本元数据与发布说明变更）；macOS、Windows、Linux 安装包由远端 CI 构建。