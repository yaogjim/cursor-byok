// Package toolresult 提供工具结果展示与模型回放共用的预算、UTF-8 截断和提示构造。
//
// 调用方必须显式选择 PurposeDisplay 或 PurposeReplay。结构化 proto/JSON 投影
// 仍留在现有使用方；本包不提供 Payload-any 转换框架或配置开关。
package toolresult
