# Cursor BYOK noad

> A local BYOK client for connecting Cursor to your own model APIs. The `noad` branch keeps the fork experience conservative by default: light theme, ads disabled, no startup update check, staged update confirmation, and release/update resources tied to `yaogjim/cursor-byok`.

[Download](https://github.com/yaogjim/cursor-byok/releases/latest) · [Issues](https://github.com/yaogjim/cursor-byok/issues) · [中文说明](./README-CN.md)

![Cursor BYOK noad dashboard](./docs/images/main.png)

## What it does

- **Bring your own model API**: connect OpenAI-, Anthropic-, or compatible endpoints to Cursor workflows.
- **Local-first gateway**: run the backend service and proxy locally, with settings stored on your machine.
- **Flexible model channels**: configure endpoint paths, model IDs, context windows, reasoning/thinking options, custom headers, and extra request parameters.
- **Agent capability preservation**: keep Cursor Agent flows such as tool calling, Skills, MCP, checkpoint handling, compaction, and streamed shell/tool output.
- **Session and token visibility**: inspect token usage, cache statistics, conversation summaries, and model test results.

## noad defaults

| Area | Default behavior | User control |
| --- | --- | --- |
| Ads | Disabled; no ad package is fetched and no top/banner/popup ad is shown | Can be explicitly enabled in local settings |
| Updates | No startup update check; check, download, and install are separate confirmations | Manual check from the footer/settings |
| Theme | Light by default | Switchable between light and dark |
| Release identity | Release repository and updater resources use `yaogjim/cursor-byok` | Do not rewrite to upstream release assets |

Core principle: **off means off**. Disabled ads should not request remote ad content, and updates should not download or install without user action.

## Quick start

1. Download the latest build from [yaogjim/cursor-byok releases](https://github.com/yaogjim/cursor-byok/releases/latest).
2. Launch Cursor BYOK noad, open **Model Settings**, and add your endpoint, API key, and model ID.
3. Test the model channel; after it passes, return to the dashboard and start the local service.
4. Open Cursor and use the configured model channel with Chat or Agent.

## Screenshots

![Model settings](./docs/images/model.png)

![Client settings](./docs/images/cursor%20model.png)

## How it works

```text
Cursor client
    │ Agent requests and tool results
    ▼
Cursor BYOK noad local service
    │ OpenAI / Anthropic compatible requests
    ▼
Your model API
```

## Relationship to upstream

This fork absorbs selected upstream fixes and capabilities while preserving the `noad` product policy above. Upstream documentation can still be useful for general setup concepts, but release downloads, updater resources, and fork-specific behavior are maintained under `yaogjim/cursor-byok`.

## License

This project is open source under the [MIT License](./LICENSE).