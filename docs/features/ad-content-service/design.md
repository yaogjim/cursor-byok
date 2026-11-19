# 广告内容服务：停用旧远端与自定义服务接口设计

- 日期：2026-11-18
- 状态：设计（本批仅执行「停用旧远端 + 设置项灰化」；自定义服务拉取未实施）
- 用户决策：不移除广告能力与界面广告位；后端不再从 `ads.leokun.cn` 拉取；设置页「广告内容」灰化、不可点击、默认关闭；后续改从自定义服务器拉取。

## 1. 现状链路（代码核对）

| 层 | 行为 | 位置 |
| --- | --- | --- |
| 启动 | 读 `advertising.enabled` 初始化广告服务 | `internal/app/runner.go` |
| 触发 | 启动后每 3 分钟刷新；主窗口获得焦点时刷新 | `internal/app/runner.go` `adRefreshInterval` |
| 拉取 | 遍历硬编码广告位（原 `ads.leokun.cn/1..3`），`GET` 返回 zip | `internal/ads/service.go` `FetchOnce`/`fetchSlotOnce` |
| 缓存 | 包内容哈希不变则跳过；404 清除对应位；包落 `data/ads` | `internal/ads/service.go` |
| 运行态 | `GetRuntime` 汇总各广告位的可用性、窗口尺寸、总览标题 | `internal/ads/service.go` |
| 展示 | 总览广告位 `home-ads`；主窗口 `AdModelProvider` iframe；资源经本机 `/ad/...` 提供 | `frontend/src/views/Home.vue`、`AdModelProvider.vue`、`internal/ads/http.go` |
| 设置 | 「内容与更新 → 广告内容」开关，回传 `advertising.enabled` | `frontend/src/views/SettingsView.vue` |

### 拉取包契约（现状）

- 响应体为 zip（上限 50MB）：根目录 `index.html`、相对静态资源（单文件上限 20MB）、`config.yaml`。
- `config.yaml` 字段：`enabled`、`window.width/height`、`home.title/subtitle`。
- 状态码：`404` 表示该广告位下线；`2xx` 为包内容；其它状态视为拉取失败。
- 请求头会携带：应用版本、OS/版本、架构、当前广告哈希、**设备 ID**、turn/token 用量、供应商数量。

## 2. 本批执行内容

1. 清空 `internal/ads` 中的远端广告位配置（`Slots` 为空），不再向 `ads.leokun.cn` 发起任何请求。
2. `/ad` 资源路由增加广告位校验：未配置广告位的请求一律返回 404，旧缓存不会继续被展示。
3. 设置页「广告内容」保持位置，开关改为禁用（灰化、不可点击），说明改为「内容服务暂未接入，广告位保留但当前不拉取、不显示」。
4. 总览广告位、`AdModelProvider` 弹窗组件保留，运行态为空时不渲染。
5. 配置默认值仍为 `advertising.enabled=false`；即使旧配置为 `true`，因没有广告位也不会拉取或展示。

## 3. 自定义服务器接口设计（后续实施）

### 3.1 配置

建议新增（默认空，不改变旧配置兼容）：

```yaml
advertising:
  enabled: false
  slots:
    - id: "1"
      url: "https://ads.example.com/1"
```

- 「启用且有至少一个非空 slots」才允许拉取；否则与当前停用行为一致。
- 保存契约约束：前端保存设置时目前只回传 `advertising.enabled`，后端为整体覆盖。新增 `slots` 前必须二选一：
  - 前端回传完整 `advertising`（推荐，语义清晰）；
  - 或后端合并时保留已有 `slots`（需明确“如何清空 slot”的途径）。
  未处理该约束会导致任何一次设置保存清空自定义地址。

### 3.2 HTTP 契约

- `GET {slot.url}`，`Accept: application/zip`。
- `200`：zip 包，结构与现状一致；`404`：该位下线并清缓存；其它状态为失败，保留旧包。
- 支持 `ETag`/`If-None-Match` 或内容哈希短路（现状按内容 SHA-256 判断，可沿用）。
- 建议响应大小、解压后路径与文件数量继续走现有上限校验，防止 zip bomb 与路径穿越。

### 3.3 上报与隐私

- 现状向内容服务器上报设备 ID 与用量统计。自定义服务默认**不应**继承该行为；建议改为配置开关或最小化字段（仅版本/平台用于兼容）。
- 所有自定义地址仅允许 `https`（本机调试可显式允许 `http://127.0.0.1`）。

### 3.4 运行态与展示

- `Runtime`/`SlotRuntime` 结构可保持不变，前端无需改动即可显示自定义内容。
- 广告位数量、窗口尺寸、总览标题继续由包内 `config.yaml` 控制。

## 4. 验收与回退

- 本批验收：`go test ./internal/ads`、`go build ./...`、前端构建与设置页结构断言通过；设置页开关禁用且点击无响应；断网/无 slots 时无任何广告请求。
- 回退：恢复 `internal/ads/types.go` 的广告位配置与设置页开关即可；本次未删除历史缓存文件，未改配置字段结构。
- 未做：自定义服务器拉取、配置字段、保存契约调整、缓存清理策略。
