# cursor-cli-docker

198 上 Cursor CLI 容器的构建上下文。基础镜像 `oven/bun:1.3.14-slim`，内置 Node.js `24.13.1`；浏览器终端用 [WeTTY](https://github.com/butlerx/wetty)（xterm.js），不用 ttyd；并预装 [agent-browser](https://github.com/vercel-labs/agent-browser) `0.34.0` 与 Chrome for Testing。

完整操作与验收见 [`docs/ops_198_cursor_cli_session_reuse.md`](../docs/ops_198_cursor_cli_session_reuse.md)。主 Agent 与 custom subagent 的独立模型路由配置见 [`subagent-pool.md`](subagent-pool.md)。不要把 token、`auth.json`、htpasswd、SSH 密码写进本目录。

## 构建

在能拉 Debian / npm 的机器上（198 需 HTTP 代理）：

```bash
cd cursor-cli-docker
docker build \
  --build-arg HTTP_PROXY=http://172.16.137.80:8118 \
  --build-arg HTTPS_PROXY=http://172.16.137.80:8118 \
  -t cursor-cli-runtime:agent-browser-smoke .
```

先按下方步骤验证候选标签，再把同一镜像提升为 `cursor-cli-runtime:wetty` 和 `cursor-cli-runtime:1.3.14-slim`。Docker 容器按镜像 ID 运行，改标签不会重启或替换已启动的 `cursor-cli`；提升前给旧镜像保留 `cursor-cli-runtime:wetty-pre-agent-browser` 标签。

## 运行要点

- `--network host`，WeTTY `--host 127.0.0.1 --port 7681`
- **不要** `-p 7681:7681`
- 容器进程用户是 root：WeTTY 非 root 时会 `ssh localhost`，在 host 网络下会打到宿主机 sshd
- 浏览器里的交互 shell 经 `cursor-cli-shell` 降权为 `bun` 的 `/bin/sh`
- TLS 与 HTTP Basic 在宿主机 nginx 终止，见 `nginx-wetty.conf.example`
- nginx 必须 `proxy_hide_header Content-Security-Policy` 并允许 `connect-src` 的 `wss:`；否则浏览器 WebSocket 会被 WeTTY 自带的 `ws://` CSP 拦掉，终端大约 10 秒断一次
- `X-Frame-Options` 必须是 `SAMEORIGIN`（不要 `DENY`），CSP 加 `frame-ancestors 'self'`：WeTTY 用同源 iframe 读 `/assets/xterm_config/index.html`，`DENY` 会拦掉这个 iframe
- CLI 与 `auth.json` 放 bind mount（`/home/bun`），不打进镜像
- `agent-browser` 固定版本安装；其 Chrome for Testing 放在 `/opt/agent-browser`，通过 `AGENT_BROWSER_EXECUTABLE_PATH` 使用，并提供标准命令名 `google-chrome` 供 `agent-browser doctor` 发现，避免被 `/home/bun` bind mount 遮住
- `agent-browser` 的会话、配置和状态仍使用 bun 的可写家目录；镜像构建阶段只固化浏览器二进制和 Linux 依赖

## agent-browser 验证

不要替换或停止正在使用的 `cursor-cli`。用新标签构建后，通过独立、一次性的容器验证：

```bash
docker run --rm --init --network host --user bun \
  --entrypoint /bin/sh cursor-cli-runtime:agent-browser-smoke -lc '
    agent-browser --version
    google-chrome --version
    agent-browser doctor --offline --quick
    agent-browser --session smoke open http://127.0.0.1:19091
    agent-browser --session smoke snapshot
    agent-browser --session smoke close
  '
```

完整验收应使用临时本地测试页覆盖导航、快照、表单输入、点击、JavaScript 结果、截图和关闭，并确认原 `cursor-cli` 容器 ID、启动时间和状态未变化。