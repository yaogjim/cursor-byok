# cursor-cli-docker

198 上 Cursor CLI 容器的构建上下文。基础镜像 `oven/bun:1.3.14-slim`，浏览器终端用 [WeTTY](https://github.com/butlerx/wetty)（xterm.js），不用 ttyd。

完整操作与验收见 [`docs/ops_198_cursor_cli_session_reuse.md`](../docs/ops_198_cursor_cli_session_reuse.md)。不要把 token、`auth.json`、htpasswd、SSH 密码写进本目录。

## 构建

在能拉 Debian / npm 的机器上（198 需 HTTP 代理）：

```bash
cd cursor-cli-docker
docker build \
  --build-arg HTTP_PROXY=http://172.16.137.80:8118 \
  --build-arg HTTPS_PROXY=http://172.16.137.80:8118 \
  -t cursor-cli-runtime:wetty .
```

## 运行要点

- `--network host`，WeTTY `--host 127.0.0.1 --port 7681`
- **不要** `-p 7681:7681`
- 容器进程用户是 root：WeTTY 非 root 时会 `ssh localhost`，在 host 网络下会打到宿主机 sshd
- 浏览器里的交互 shell 经 `cursor-cli-shell` 降权为 `bun` 的 `/bin/sh`
- TLS 与 HTTP Basic 在宿主机 nginx 终止，见 `nginx-wetty.conf.example`
- nginx 必须 `proxy_hide_header Content-Security-Policy` 并允许 `connect-src` 的 `wss:`；否则浏览器 WebSocket 会被 WeTTY 自带的 `ws://` CSP 拦掉，终端大约 10 秒断一次
- `X-Frame-Options` 必须是 `SAMEORIGIN`（不要 `DENY`），CSP 加 `frame-ancestors 'self'`：WeTTY 用同源 iframe 读 `/assets/xterm_config/index.html`，`DENY` 会拦掉这个 iframe
- CLI 与 `auth.json` 放 bind mount（`/home/bun`），不打进镜像

示例：`run.example.sh`