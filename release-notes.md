# Cursor助手 v0.0.45

- 同步上游 `0.0.45`，回退会导致会话消失的 blob/checkpoint 同步实现。
- 增加由 `CURSOR_BYOK_DISABLE_WEBVIEW_SANDBOX` 显式控制的 Windows VDI WebView2 白屏规避选项；默认保持 sandbox。
- 更新检查、下载资源校验和发布清单统一指向 `yaogjim/cursor-byok`。
- 保留本地默认浅色、默认关闭广告、默认关闭启动更新检查，以及更新检查、下载、安装分阶段确认。