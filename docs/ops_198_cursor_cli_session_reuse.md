# 198 上非官方复用本机 CLI 会话，并在 Docker 中安装 Cursor CLI

- **文档类型**：操作手册（runbook）
- **状态**：已按 2026-08-24 实跑落地；非官方路径，官方不支持
- **CLI 版本**：`2026.08.11-e8db854`
- **基础镜像**：`oven/bun:1.3.14-slim`
- **运行镜像**：`cursor-cli-runtime:wetty`（同 tag `cursor-cli-runtime:1.3.14-slim`；由 `oven/bun:1.3.14-slim` 构建，含 WeTTY / wget / git 等）
- **远端**：`jandar@172.16.23.198`
- **决策锚点**：[`prd_cursor_byok_工作决策基线.md`](prd_cursor_byok_工作决策基线.md) §3.2、§3.3

本文记录三件事如何接在一起：

1. 把本机 macOS Cursor CLI 的 **access token + refresh token** 写成 Linux `auth.json`（对话里称为「方案 2：非官方会话复用」）。
2. 在 198 的 Docker（`oven/bun:1.3.14-slim`）里安装 Cursor CLI，用这份会话工作；再配出网 HTTP 代理，以及后续把本机 `127.0.0.1:18090` 接到容器（方案 A）。
3. 用 [WeTTY](https://github.com/butlerx/wetty)（xterm.js）提供浏览器终端：交互 `/bin/sh` 跑在非 root 用户 `bun` 下；WeTTY **只**听 `127.0.0.1:7681`，交给宿主机已有 nginx 的 HTTPS + HTTP Basic。构建文件在仓库 [`cursor-cli-docker/`](../cursor-cli-docker/)。已弃用 ttyd。

不要把真实 token、refresh token、SSH 密码、`auth.json` 正文、htpasswd、Basic Auth 口令贴进聊天、提交进 git，或写进本文件。

## 1. 先分清三条路

| 路径 | 官方是否支持 | 用途 | 关键材料 |
|------|--------------|------|----------|
| A. `CURSOR_API_KEY` / `--api-key` | 支持 | CI / 无浏览器环境 | Dashboard API key |
| B. `agent login` 浏览器 / 设备码 | 支持 | 本机交互登录 | 短时 access + 可续期 refresh，写入本机凭据库 |
| C. 把 B 的 access + refresh 拷到 Linux `auth.json` | **不支持** | 让远端 CLI 复用本机已登录会话 | `accessToken` + `refreshToken` |

官方文档没有「把桌面/CLI 会话转换成 API key」的入口。`refresh_token`、`WorkosCursorSessionToken`、`client_id`、账号密码都 **不是** CLI API key。

本次用户明确选了 **C**。C 只是把 B 已经拿到的会话搬到 Linux 文件存储。Access token 会过期，refresh token 负责续期；拷过去的是这一对，不是 API key。

先前对话里出现过真实会话材料，按泄露处理：不要重用聊天里的旧值，不要再打印。需要时从本机 Keychain **重新导出**。

## 2. CLI 凭据实际存在哪

依据本机 CLI `2026.08.11-e8db854` 的 `cli-credentials`：

环境变量 `AGENT_CLI_CREDENTIAL_STORE`：

- `file`：强制文件存储
- `memory`：内存
- 其它 / 未设：`default`

`default` 的平台行为：

- **macOS**：Keychain（不是把 `~/Library/Keychains` 整份拷到 Linux）
- **Linux / Docker**：文件 `auth.json`

Keychain 条目（domain 固定为 `cursor`）：

| 用途 | account | service |
|------|---------|---------|
| access token | `cursor-user` | `cursor-access-token` |
| refresh token | `cursor-user` | `cursor-refresh-token` |

文件存储路径：

| 系统 | 路径 |
|------|------|
| Linux | `${XDG_CONFIG_HOME:-$HOME/.config}/cursor/auth.json` |
| macOS 且 `AGENT_CLI_CREDENTIAL_STORE=file` | `~/.cursor/auth.json` |
| Windows | `%APPDATA%\Cursor\auth.json` |

Linux 容器里要用的就是：

```text
/root/.config/cursor/auth.json
```

最小 JSON（字段名必须是 camelCase）：

```json
{
  "accessToken": "<access>",
  "refreshToken": "<refresh>"
}
```

可选字段还有 `apiKey`、`bedrockCredentials`。本次只拷会话对，不写 API key。

目录权限 `700`，文件权限 `600`。CLI 自己写入时也是这个模式（目录 `0o700`，文件 `0o600`）。

把 macOS Keychain 数据库文件拷到 Linux **无效**。必须读出两个 secret，写成上面这份 JSON。

## 3. 本机导出会话（不打印 token）

前置：本机已 `agent login` / `agent status` 显示已登录。Keychain 未锁：

```bash
security unlock-keychain
```

确认条目存在（这两条 **不会** 打印 secret 正文）：

```bash
security find-generic-password -a cursor-user -s cursor-access-token >/dev/null
security find-generic-password -a cursor-user -s cursor-refresh-token >/dev/null
```

用 Python 写成临时文件，避免 `security -w` 把 token 打到终端：

```bash
umask 077
mkdir -p /tmp/cursor-cli-session-xfer
python3 - <<'PY'
import json, subprocess, os
def secret(service):
    p = subprocess.run(
        ["security", "find-generic-password", "-a", "cursor-user", "-s", service, "-w"],
        check=True, capture_output=True, text=True,
    )
    return p.stdout.strip()
data = {
    "accessToken": secret("cursor-access-token"),
    "refreshToken": secret("cursor-refresh-token"),
}
path = "/tmp/cursor-cli-session-xfer/auth.json"
os.makedirs(os.path.dirname(path), exist_ok=True)
with open(path, "w", encoding="utf-8") as f:
    json.dump(data, f, indent=2)
    f.write("\n")
os.chmod(path, 0o600)
st = os.stat(path)
print("wrote", path, "bytes", st.st_size, "keys", sorted(data), "access_len", len(data["accessToken"]), "refresh_len", len(data["refreshToken"]))
PY
```

只允许打印路径、字节数、字段名、长度。禁止 `cat` / `jq .` 这份文件。

2026-08-24 实跑：导出文件约 894 字节，字段只有 `accessToken`、`refreshToken`。

## 4. 传到 198，再放进容器 root

198 上 Docker 默认数据目录在 `/home/docker`，`/` 与 `/home` 空间紧，**容器持久数据放 `/usr`**（约 68G 可用）。

```bash
# 本机 → 198 用户目录（先落一份备份）
scp /tmp/cursor-cli-session-xfer/auth.json jandar@172.16.23.198:/home/jandar/.config/cursor/auth.json
```

远端：

```bash
mkdir -p /home/jandar/.config/cursor
chmod 700 /home/jandar/.config /home/jandar/.config/cursor
chmod 600 /home/jandar/.config/cursor/auth.json

sudo mkdir -p \
  /usr/local/cursor-cli/root/.config/cursor \
  /usr/local/cursor-cli/root/.local/bin \
  /usr/local/cursor-cli/root/.local/share/cursor-agent/versions \
  /usr/local/cursor-cli/workspace

sudo install -m 600 -o root -g root \
  /home/jandar/.config/cursor/auth.json \
  /usr/local/cursor-cli/root/.config/cursor/auth.json

sudo chown -R root:root /usr/local/cursor-cli/root
sudo chown jandar:jandar /usr/local/cursor-cli /usr/local/cursor-cli/workspace
sudo chmod 700 /usr/local/cursor-cli /usr/local/cursor-cli/root
```

只检查字段是否存在，不打印值：

```bash
sudo python3 - <<'PY'
import json, os
p="/usr/local/cursor-cli/root/.config/cursor/auth.json"
d=json.load(open(p))
print("keys", sorted(d))
print("bytes", os.path.getsize(p))
print("has_access", bool(d.get("accessToken")))
print("has_refresh", bool(d.get("refreshToken")))
PY
```

传完后删除本机临时文件：

```bash
rm -rf /tmp/cursor-cli-session-xfer
```

## 5. 198 出网 HTTP 代理

198 直连外网经常被拒。安装 CLI、拉官方模型列表时走：

```text
http://172.16.137.80:8118
```

同时设置：

```bash
PROXY=http://172.16.137.80:8118
NO_PROXY=127.0.0.1,localhost,::1,172.16.0.0/12,192.168.0.0/16
export http_proxy=$PROXY https_proxy=$PROXY
export HTTP_PROXY=$PROXY HTTPS_PROXY=$PROXY ALL_PROXY=$PROXY
export no_proxy=$NO_PROXY NO_PROXY=$NO_PROXY
# Node CLI 另认这两项
export GLOBAL_AGENT_HTTP_PROXY=$PROXY
export GLOBAL_AGENT_HTTPS_PROXY=$PROXY
```

`NO_PROXY` 必须包含 `127.0.0.1`，否则后面走本机 `18090` 时会被 HTTP 代理拐走。`172.16.0.0/12` 覆盖 Docker 网桥 `172.17.0.1`。

`jandar` 不在 `docker` 组，一律 `sudo docker`。SSH 若密码登录失败，加上：

```text
PreferredAuthentications=password
PubkeyAuthentication=no
```

（后来已改为本机公钥登录 198，见 §9。）

## 6. 创建容器并安装 CLI

镜像已在 198 拉取：`oven/bun:1.3.14-slim`（Debian 13 trixie，约 170MB）。该镜像 **没有** `curl`/`wget`。

### 6.1 首次创建（官方会话验证阶段用 bridge）

早期为了先验证 `auth.json`，容器是 bridge 网络。当前落地已改为 `--network host`（§9）。下面给出 **当前** 应使用的 `docker run`，避免再走一遍已废弃的 bridge。

```bash
sudo docker rm -f cursor-cli 2>/dev/null || true
sudo docker run -d --name cursor-cli --restart unless-stopped --network host \
  -v /usr/local/cursor-cli/root:/root \
  -v /usr/local/cursor-cli/workspace:/workspace \
  -w /workspace \
  -e AGENT_CLI_CREDENTIAL_STORE=file \
  -e NO_OPEN_BROWSER=1 \
  -e PATH=/root/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/bun-node-fallback-bin \
  -e http_proxy=http://172.16.137.80:8118 \
  -e https_proxy=http://172.16.137.80:8118 \
  -e HTTP_PROXY=http://172.16.137.80:8118 \
  -e HTTPS_PROXY=http://172.16.137.80:8118 \
  -e ALL_PROXY=http://172.16.137.80:8118 \
  -e GLOBAL_AGENT_HTTP_PROXY=http://172.16.137.80:8118 \
  -e GLOBAL_AGENT_HTTPS_PROXY=http://172.16.137.80:8118 \
  -e no_proxy=127.0.0.1,localhost,::1,172.16.0.0/12,192.168.0.0/16 \
  -e NO_PROXY=127.0.0.1,localhost,::1,172.16.0.0/12,192.168.0.0/16 \
  oven/bun:1.3.14-slim sleep infinity
```

若当时还在验证官方模型列表、尚未接 18090，不要加 `CURSOR_API_ENDPOINT`。接上方案 A 之后再加，见 §9。

`/root` 是 bind mount，CLI 装在 `/root/.local` 上，落在 `/usr` 大盘，不占满 `/home`。

不要用 `bash -lc` 跑 `agent`：login shell 会重置 `PATH`，找不到 `~/.local/bin/agent`。用：

```bash
sudo docker exec cursor-cli /root/.local/bin/agent ...
```

或把 `/root/.local/bin` 写进容器 `PATH`（上面已写）。

### 6.2 容器内装 curl，再装 CLI

```bash
sudo docker exec cursor-cli bash -c 'set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
printf "Acquire::http::Proxy \"%s\";\nAcquire::https::Proxy \"%s\";\n" "$http_proxy" "$https_proxy" > /etc/apt/apt.conf.d/99proxy
apt-get update -o Acquire::Retries=3
apt-get install -y --no-install-recommends curl ca-certificates
'
```

官方安装脚本：

```bash
curl https://cursor.com/install -fsS | bash
```

它会检测 `linux/x64`，下载：

```text
https://downloads.cursor.com/lab/2026.08.11-e8db854/linux/x64/agent-cli-package.tar.gz
```

安装结果：

- 版本目录：`~/.local/share/cursor-agent/versions/2026.08.11-e8db854/`
- 包装脚本：`cursor-agent`（约 1KB），内嵌 `node`（约 124MB）+ `index.js`
- 符号链接：`~/.local/bin/agent`、`~/.local/bin/cursor-agent`

实跑中该 tar 经代理会在约 85% 被掐断（`SSL unexpected eof`）。不要 `curl | tar` 一次流式解压。改成落盘并 `--retry`：

```bash
sudo docker exec cursor-cli bash -c 'set -euo pipefail
curl -fL --retry 8 --retry-all-errors --retry-delay 3 --continue-at - \
  -o /tmp/agent-cli-package.tar.gz \
  https://downloads.cursor.com/lab/2026.08.11-e8db854/linux/x64/agent-cli-package.tar.gz
gzip -t /tmp/agent-cli-package.tar.gz
FINAL=/root/.local/share/cursor-agent/versions/2026.08.11-e8db854
mkdir -p "$FINAL" /root/.local/bin
tar --strip-components=1 -xzf /tmp/agent-cli-package.tar.gz -C "$FINAL"
ln -sfn "$FINAL/cursor-agent" /root/.local/bin/agent
ln -sfn "$FINAL/cursor-agent" /root/.local/bin/cursor-agent
'
```

也可以在本机下好同一 URL（约 81MB），`scp` 到 `/usr/local/cursor-cli/agent-cli-package.tar.gz`，再拷进容器解压。`gzip -t` 通过后再解压。

`oven/bun`  overlay 里的 `curl` 会随 `docker rm` 丢失；CLI 本体在 bind mount 的 `/root` 上，重建容器不必重装 CLI。

包装脚本在 `AGENT_CLI_CREDENTIAL_STORE=file` 时 **不会** 给 Node 加 `--use-system-ca`。这是该版本的既有行为，不是额外开关。

## 7. 用非官方会话打官方 Cursor（不经 18090）

容器内 **不要** 设 `CURSOR_API_ENDPOINT`，**不要** 设 `CURSOR_API_KEY`。只靠 `auth.json` + `AGENT_CLI_CREDENTIAL_STORE=file`。

```bash
sudo docker exec -e AGENT_CLI_CREDENTIAL_STORE=file cursor-cli agent status
sudo docker exec -e AGENT_CLI_CREDENTIAL_STORE=file cursor-cli agent models
```

2026-08-24 验证：

- `agent status`：已登录（本机同一会话）
- `agent models`：官方 **204** 个模型 ID，与本机 `agent models` **完全一致（含顺序）**
- 未指向 `127.0.0.1:18090`，未改本机 18080/18090

宿主机包装脚本（当时）：

```bash
#!/bin/bash
exec sudo docker exec -e AGENT_CLI_CREDENTIAL_STORE=file cursor-cli /root/.local/bin/agent "$@"
```

路径：`/usr/local/cursor-cli/agent`。接上 18090 后应同时导出 `CURSOR_API_ENDPOINT`，见 §9。

## 8. 为什么本机 18090 不能直接给 198 用

本机 cursor-byok backend / MITM 默认：

- backend：`127.0.0.1:18090`
- MITM：`127.0.0.1:18080`

只绑 loopback。本机局域网 IP 是 `192.168.8.239`。198 **ping 不通** 该地址。本机远程登录 / `sshd` 默认未开。

因此：

```bash
ssh -N -L 18090:127.0.0.1:18090 用户名@Mac局域网IP
```

从 198 发起这条 **正向** `-L`：既要本机开 SSH 和本机账号，又没有三层路由。不可用。

也不要把 18090 改绑 `0.0.0.0`：暴露面变大，198 仍然到不了这台 Mac，且违反运行中实例保护。

用户确认的接法是 **方案 A**：Mac 主动反向隧道到 198，容器 `--network host`，访问 198 自己的 `127.0.0.1:18090`。

## 9. 方案 A：反向隧道 + host 网络吃本机 18090

不登本机、不用本机用户名密码。本机用公钥登 198 的 `jandar`。

### 9.1 本机 → 198 公钥

```bash
ssh-keygen -t ed25519 -f ~/.ssh/id_ed25519_cursor198 -N '' -C 'cursor198-18090-tunnel'
```

把 `.pub` 追加到 198 `~/.ssh/authorized_keys`（目录 `700`，文件 `600`）。本机 `~/.ssh/config`：

```sshconfig
Host cursor-198
  HostName 172.16.23.198
  User jandar
  IdentityFile ~/.ssh/id_ed25519_cursor198
  IdentitiesOnly yes
  ServerAliveInterval 30
  ServerAliveCountMax 3
```

```bash
ssh -o BatchMode=yes cursor-198 'echo KEY_LOGIN_OK'
```

### 9.2 LaunchAgent 常驻反向隧道

198 的 `sshd`：`AllowTcpForwarding yes`，`GatewayPorts no`，`PermitListen any`。`GatewayPorts no` 时 `-R` 只绑在 **远端 loopback**，这正是 host 网络容器需要的。

本机 plist：`~/Library/LaunchAgents/com.yaogj.cursor198-18090-tunnel.plist`

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.yaogj.cursor198-18090-tunnel</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/ssh</string>
		<string>-N</string>
		<string>-o</string>
		<string>BatchMode=yes</string>
		<string>-o</string>
		<string>ExitOnForwardFailure=yes</string>
		<string>-o</string>
		<string>ServerAliveInterval=30</string>
		<string>-o</string>
		<string>ServerAliveCountMax=3</string>
		<string>-o</string>
		<string>IdentitiesOnly=yes</string>
		<string>-i</string>
		<string>/Users/yaogj/.ssh/id_ed25519_cursor198</string>
		<string>-R</string>
		<string>127.0.0.1:18090:127.0.0.1:18090</string>
		<string>jandar@172.16.23.198</string>
	</array>
	<key>KeepAlive</key>
	<true/>
	<key>RunAtLoad</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>/Users/yaogj/Library/Logs/cursor198-18090-tunnel.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/yaogj/Library/Logs/cursor198-18090-tunnel.log</string>
</dict>
</plist>
```

加载：

```bash
UID_NUM=$(id -u)
launchctl bootstrap "gui/${UID_NUM}" ~/Library/LaunchAgents/com.yaogj.cursor198-18090-tunnel.plist
```

等价命令：

```bash
ssh -N -o ExitOnForwardFailure=yes \
  -R 127.0.0.1:18090:127.0.0.1:18090 \
  jandar@172.16.23.198
```

未确认前 **不转发 18080**。本机 18080/18090 仍由原来的 Cursor 进程监听，不得停止或替换。

隧道起来后，198 宿主机：

```bash
curl -sS http://127.0.0.1:18090/healthz
# 期望：ok
ss -ltn | grep 18090
# 期望：127.0.0.1:18090
```

### 9.3 容器改 host 网络并指向 18090

Docker bridge 里的 `127.0.0.1` 不是宿主机。方案 A 用 `--network host`，容器与宿主机共用网络，于是 `http://127.0.0.1:18090` 就是反向隧道。

在 §6.1 的 `docker run` 上增加：

```bash
-e CURSOR_API_ENDPOINT=http://127.0.0.1:18090
```

**不要** 设 `--api-key` / `CURSOR_API_KEY`。本地 BYOK 用会话文件即可。

更新包装脚本：

```bash
#!/bin/bash
exec sudo docker exec \
  -e AGENT_CLI_CREDENTIAL_STORE=file \
  -e CURSOR_API_ENDPOINT=http://127.0.0.1:18090 \
  cursor-cli /root/.local/bin/agent "$@"
```

`/root/.bashrc` / `.profile`（bind mount，重建容器仍在）：

```bash
export PATH="/root/.local/bin:$PATH"
export AGENT_CLI_CREDENTIAL_STORE=file
export NO_OPEN_BROWSER=1
```

### 9.4 BYOK 验收

```bash
# 本机 18090 仍在、healthz ok
curl -sS http://127.0.0.1:18090/healthz

# 198 宿主机与容器
ssh cursor-198 'curl -sS http://127.0.0.1:18090/healthz'
sudo docker inspect cursor-cli --format '{{.HostConfig.NetworkMode}}'
sudo docker exec cursor-cli agent models
```

2026-08-24 验证：

- 远端与容器 `/healthz` 均为 `ok`
- 容器 `NetworkMode=host`，`CURSOR_API_ENDPOINT=http://127.0.0.1:18090`
- `agent models` 与本机 `CURSOR_API_ENDPOINT=http://127.0.0.1:18090 agent models` 的 **21** 个模型 ID 完全一致（不是官方 204）
- 本机 `127.0.0.1:18080` / `18090` 仍是原 Cursor 进程
- 连上 BYOK 后 `agent status` 显示的是本地 backend 登录身份，不再是官方账号，这是预期

Mac 休眠后隧道会断；唤醒后 LaunchAgent `KeepAlive` 会重连。

## 10. 两种工作模式怎么切换

同一份 `auth.json`，靠 `CURSOR_API_ENDPOINT` 切换：

| 模式 | `CURSOR_API_ENDPOINT` | 出网 | `agent models` 形态 |
|------|----------------------|------|---------------------|
| 官方 Cursor | 不设 | HTTP 代理 `172.16.137.80:8118` | 官方约 204 个 |
| 本机 BYOK | `http://127.0.0.1:18090` | 18090 走 `NO_PROXY`；模型上游仍由本机 byok 决定 | 本机配置的那批（本次 21 个） |

当前容器默认是 **BYOK**。要临时打官方列表：重建容器时去掉 `CURSOR_API_ENDPOINT`，或：

```bash
sudo docker exec -e CURSOR_API_ENDPOINT= cursor-cli agent models
```

（空值是否能覆盖镜像环境变量，取决于 Docker/exec 行为；不可靠时用 `docker rm` + 不带该变量的 `docker run`。）

可靠切换：改 `docker run` 环境变量后重建容器。CLI 在 `/root` bind mount 上，不必重装。

## 11. 日常使用

在 198：

```bash
sudo docker exec cursor-cli agent status
sudo docker exec cursor-cli agent models
```

浏览器终端见 §13：打开 `https://172.16.23.198/`，接受自签证书警告后用 HTTP Basic 登录，进到 WeTTY（xterm.js），shell 是 `bun` 的 `/bin/sh`，直接跑 `agent`。

看隧道：

```bash
launchctl print gui/$(id -u)/com.yaogj.cursor198-18090-tunnel
```

会话失效时（`agent status` 未登录、refresh 失败）：在本机重新按 §3 导出，覆盖 198 的 `auth.json`（至少 `/usr/local/cursor-cli/home/.config/cursor/auth.json`；若仍用 root 家目录备份则同时覆盖 `/usr/local/cursor-cli/root/.config/cursor/auth.json`），不要从旧聊天记录里粘贴。

## 12. 本次落地清单

| 项 | 值 |
|----|----|
| 基础镜像 | `oven/bun:1.3.14-slim` |
| 运行镜像 | `cursor-cli-runtime:wetty`（同 digest 的 tag `cursor-cli-runtime:1.3.14-slim`，约 900MB） |
| 构建上下文 | 仓库 [`cursor-cli-docker/`](../cursor-cli-docker/) |
| 容器名 | `cursor-cli` |
| WeTTY 进程用户 | root（必须；见 §13.3） |
| 交互 shell | `bun`（uid 1000）的 `/bin/sh` |
| 网络 | `host` + `--init` |
| 重启策略 | `unless-stopped` |
| 数据根 | `/usr/local/cursor-cli`（`/root`、`/workspace`、`/home/bun` bind） |
| 浏览器会话文件 | `/usr/local/cursor-cli/home/.config/cursor/auth.json` |
| root 备份会话 | `/usr/local/cursor-cli/root/.config/cursor/auth.json` |
| 凭据库 | `AGENT_CLI_CREDENTIAL_STORE=file` |
| CLI（bun） | `/home/bun/.local/bin/agent` → `.../versions/2026.08.11-e8db854/cursor-agent` |
| 出网代理 | `http://172.16.137.80:8118` + `GLOBAL_AGENT_*` |
| BYOK | `CURSOR_API_ENDPOINT=http://127.0.0.1:18090` |
| 隧道 | 本机 LaunchAgent `com.yaogj.cursor198-18090-tunnel` |
| 本机 SSH 别名 | `cursor-198` |
| WeTTY | `127.0.0.1:7681`，xterm.js，命令 `cursor-cli-shell` → bun `/bin/sh`，**不**发布到 `0.0.0.0` |
| 浏览器入口 | `https://172.16.23.198/`（宿主机 nginx：HTTPS + HTTP Basic → `127.0.0.1:7681`） |
| 已弃用 | ttyd（备份镜像 tag `cursor-cli-runtime:ttyd-backup`） |
| 未做 | 开本机 sshd、转发 18080、改绑 18090、官方 API key、把 7681 映射出容器/防火墙放行 |

Docker 引擎 data-root 仍是 `/home/docker`。镜像层和 overlay 在 `/home`；CLI 与 `auth.json` 在 `/usr` bind mount。不要把大文件写进容器 overlay。

## 13. 浏览器终端：WeTTY + 宿主机 nginx

ttyd 的浏览器交互差，已改用 [WeTTY](https://github.com/butlerx/wetty)（xterm.js + WebSocket）。仍只把 `127.0.0.1:7681` 交给宿主机 HTTPS + HTTP Basic；不把 7681 直接暴露。构建文件在 [`cursor-cli-docker/`](../cursor-cli-docker/)。

### 13.1 当前事实（2026-08-24 WeTTY 验收）

| 检查 | 结果 |
|------|------|
| 容器镜像 | `cursor-cli-runtime:wetty` / `cursor-cli-runtime:1.3.14-slim`（`866c7dec7c30`，约 900MB） |
| 容器用户 / 入口 | `User=root`，`Entrypoint=/usr/local/bin/docker-entrypoint.sh`，`--init` |
| WeTTY | `wetty@2.5.0` + Node `v22.17.0`；`wetty --host 127.0.0.1 --port 7681 --base / --command /usr/local/bin/cursor-cli-shell` |
| 7681 监听 | 仅 `127.0.0.1:7681`；Docker `Ports={}`，没有 `-p 7681`；**无 ttyd 进程** |
| 页面 | loopback 与 HTTPS 鉴权后 HTML 含 WeTTY / xterm，不含 ttyd |
| 工具 | 容器内 `wget` `git` `curl` 等仍在 PATH |
| bun 家目录 | bind：`/usr/local/cursor-cli/home:/home/bun` |
| PTY 实测 | `id` → `uid=1000(bun)`，`HOME=/home/bun`，`agent` 在 PATH |
| `agent status`（bun） | 已登录；`CURSOR_API_ENDPOINT=http://127.0.0.1:18090`，`AGENT_CLI_CREDENTIAL_STORE=file` |
| HTTPS 无凭据 / 错凭据 | **401** |
| HTTPS 正确凭据 | **200**（WeTTY 页面；`/client/wetty.js` 200） |
| HTTPS CSP | nginx 隐藏 WeTTY 的 `ws://` CSP，改为 `connect-src 'self' ws: wss:`，并带 `frame-src 'self'; frame-ancestors 'self'`（否则浏览器 wss 被拦，PTY 约 10 秒被杀） |
| HTTPS X-Frame-Options | `SAMEORIGIN`（不能 `DENY`：WeTTY 用同源 iframe 加载 `/assets/xterm_config/index.html`） |
| HTTPS WebSocket | `/socket.io/` → **101**；经 nginx 的 socket.io 会话可维持 ≥20s，PTY 提示 `$ ` |
| 原 nginx :80 | 仍 200 |
| 本机 18080/18090 | 仍由原 Cursor 进程监听；远端 `/healthz` 为 `ok` |

宿主机 `iptables` 的 `INPUT` 策略是 `ACCEPT`。7681 的防护靠 **只绑 loopback**。443 对能路由到 198 的地址可达，身份门在 nginx HTTP Basic。

### 13.2 为什么 WeTTY 进程是 root

WeTTY 非 root 时会 `ssh localhost`。容器是 `--network host`，那会打到 **198 宿主机 sshd**，不是容器里的 bun shell。

WeTTY 只有 uid=0 且目标是 localhost 时才走本地 command（`loginOptions`），不走 ssh。因此：

- 容器里 **WeTTY 以 root 监听** `127.0.0.1:7681`
- `--command /usr/local/bin/cursor-cli-shell` 用 `setpriv` 把 PTY 降权为 `bun` 再 `exec /bin/sh`
- 不要给这个容器加 `--user bun`，不要给 WeTTY 加 `--force-ssh`

### 13.3 镜像怎么构建

在 198（需 HTTP 代理）：

```bash
cd /usr/local/cursor-cli/src/cursor-cli-docker   # 或仓库 cursor-cli-docker/
sudo docker build \
  --build-arg HTTP_PROXY=http://172.16.137.80:8118 \
  --build-arg HTTPS_PROXY=http://172.16.137.80:8118 \
  -t cursor-cli-runtime:wetty .
```

不要 `apt-get install npm`：Debian 的 `npm` 会拉几百个 `node-*` 包，经 8118 容易 502。Dockerfile 改为官方 Node linux-x64 二进制 + `npm install -g wetty@2.5.0`。

### 13.4 bun 家目录里的 CLI 与会话

| 路径 | 用途 |
|------|------|
| `/usr/local/cursor-cli/home/.local/bin/agent` | bun 的 CLI 入口 |
| `/usr/local/cursor-cli/home/.config/cursor/auth.json` | 文件凭据库（600） |
| `/usr/local/cursor-cli/home/.cursor/cli-config.json` | CLI 配置 |

覆盖会话时改 bun 这份 `auth.json`。root 那份只是早期备份。

### 13.5 宿主机 nginx

仍用 198 已有 `/usr/local/nginx`。原 80 不动。vhost 文件名仍是历史遗留的 `cursor-cli-ttyd.conf`，后端已是 WeTTY：

| 文件 | 作用 |
|------|------|
| `/usr/local/nginx/conf/sites.d/cursor-cli-ttyd.conf` | 443 vhost，反代 `http://127.0.0.1:7681` |
| `/usr/local/nginx/conf/cursor-cli-ttyd/tls.crt` + `tls.key` | 自签证书 |
| `/usr/local/nginx/conf/cursor-cli-ttyd/htpasswd` | HTTP Basic |
| `nginx.conf` 的 `map $http_upgrade $connection_upgrade` | WebSocket Upgrade |

读写超时 43200s。必须 `proxy_hide_header Content-Security-Policy`，并自行下发允许 `wss:` 的 CSP：WeTTY helmet 按 `req.protocol` 生成 `connect-src`，经 HTTP 反代时只会放行 `ws://`，浏览器实际连的是 `wss://`，终端会约 10 秒断一次。`X-Frame-Options` 必须是 `SAMEORIGIN`（并带 CSP `frame-ancestors 'self'`），不要 `DENY`：WeTTY 会把 `/assets/xterm_config/index.html` 嵌进同源 iframe，`configureTerm` 再读 `contentDocument`；`DENY` 会让 iframe 加载失败，随后抛 `SecurityError`。示例（无密钥）见 `cursor-cli-docker/nginx-wetty.conf.example`。

浏览器：`https://172.16.23.198/`。自签证书需手动继续，然后输入 HTTP Basic。若之前已经打开过旧页面，请强制刷新（否则旧 CSP 还在）。口令：

```bash
sudo cat /usr/local/cursor-cli/proxy/basic-auth.txt
```

不要把口令写进仓库或本文件。

### 13.6 当前容器怎么重建

CLI / `auth.json` / 工作区在 bind mount。示例脚本：`cursor-cli-docker/run.example.sh`。

```bash
sudo docker rm -f cursor-cli
sudo docker run -d --name cursor-cli --restart unless-stopped --network host --init \
  -v /usr/local/cursor-cli/root:/root \
  -v /usr/local/cursor-cli/workspace:/workspace \
  -v /usr/local/cursor-cli/home:/home/bun \
  -w /workspace \
  -e HOME=/home/bun \
  -e AGENT_CLI_CREDENTIAL_STORE=file \
  -e NO_OPEN_BROWSER=1 \
  -e PATH=/home/bun/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/bun-node-fallback-bin \
  -e CURSOR_API_ENDPOINT=http://127.0.0.1:18090 \
  -e http_proxy=http://172.16.137.80:8118 \
  -e https_proxy=http://172.16.137.80:8118 \
  -e HTTP_PROXY=http://172.16.137.80:8118 \
  -e HTTPS_PROXY=http://172.16.137.80:8118 \
  -e ALL_PROXY=http://172.16.137.80:8118 \
  -e GLOBAL_AGENT_HTTP_PROXY=http://172.16.137.80:8118 \
  -e GLOBAL_AGENT_HTTPS_PROXY=http://172.16.137.80:8118 \
  -e no_proxy=127.0.0.1,localhost,::1,172.16.0.0/12,192.168.0.0/16 \
  -e NO_PROXY=127.0.0.1,localhost,::1,172.16.0.0/12,192.168.0.0/16 \
  cursor-cli-runtime:wetty
```

**不要**加 `-p 7681:7681`，**不要** `--user bun`。

### 13.7 已知边界

- 自签证书。私钥不要进 git。
- 198 `INPUT ACCEPT`；443 靠 TLS + Basic Auth，不是 IP 白名单。
- 已认证的浏览器会话等于 bun 的交互 shell。
- WeTTY 服务进程是容器 root；PTY 已降权。host 网络下不要在容器里再起 sshd。
- Mac 休眠仍会掐断 18090 隧道。
- 本机 18080/18090 保护仍然有效。
- WeTTY 在 HTTP 反代后默认 CSP 只允许 `ws://`。已在宿主机 nginx 隐藏该头并允许 `wss:`；镜像内 `patch-wetty.cjs` 同时给 WeTTY 打了双 scheme + `trust proxy`。已打开的旧标签页需强制刷新。
- WeTTY 需要同源 iframe 加载 xterm 配置。nginx 已用 `X-Frame-Options SAMEORIGIN` + CSP `frame-ancestors 'self'`；不要改回 `DENY`，也不要清空该头。
- 旧 ttyd 镜像保留为 `cursor-cli-runtime:ttyd-backup`，当前未使用。
