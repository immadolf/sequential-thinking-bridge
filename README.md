# sequential-thinking-bridge

Go 原生的本地常驻 MCP HTTP 服务，用来替代 `npx -y @modelcontextprotocol/server-sequential-thinking` 的多进程 Node 形态。

## 目标

- 只实现 `sequentialthinking` 一个 MCP tool。
- 监听本机地址，默认 `127.0.0.1:38989`。
- 用 `Mcp-Session-Id` 做会话隔离，每个 session 拥有独立 `thoughtHistory` 和 `branches`。
- 用 idle TTL 清理长期不用的 session，默认 `2h`。
- 默认像原版一样把格式化 thought 写到 `stderr`；设置 `DISABLE_THOUGHT_LOGGING=true` 后关闭。
- 不访问外部网络，不写业务数据，不改变 Codex 全局配置。

## 构建

```bash
go test ./...
go build -o sequential-thinking-bridge ./cmd/sequential-thinking-bridge
```

## Release 下载

发布页：https://github.com/immadolf/sequential-thinking-bridge/releases

当前 Release 会提供以下平台产物：

- `sequential-thinking-bridge-darwin-arm64`：macOS Apple Silicon
- `sequential-thinking-bridge-darwin-amd64`：macOS Intel
- `sequential-thinking-bridge-linux-arm64`
- `sequential-thinking-bridge-linux-amd64`
- `sequential-thinking-bridge-windows-amd64.exe`

macOS Apple Silicon 示例：

```bash
mkdir -p ~/opt/sequential-thinking-bridge
curl -L \
  -o ~/opt/sequential-thinking-bridge/sequential-thinking-bridge \
  https://github.com/immadolf/sequential-thinking-bridge/releases/download/v0.1.0/sequential-thinking-bridge-darwin-arm64
chmod +x ~/opt/sequential-thinking-bridge/sequential-thinking-bridge
```

macOS Intel 用户把下载文件名替换为 `sequential-thinking-bridge-darwin-amd64`。

如果通过浏览器下载后被 macOS quarantine 拦截，可以执行：

```bash
xattr -d com.apple.quarantine ~/opt/sequential-thinking-bridge/sequential-thinking-bridge
```

## 运行

```bash
./sequential-thinking-bridge serve \
  --listen 127.0.0.1:38989 \
  --path /mcp \
  --session-ttl 2h
```

关闭 thought 日志：

```bash
DISABLE_THOUGHT_LOGGING=true ./sequential-thinking-bridge serve
```

可选 Bearer token：

```bash
./sequential-thinking-bridge serve --token '<token>'
```

## macOS LaunchAgent 启动

将二进制放到固定路径：

```bash
mkdir -p ~/opt/sequential-thinking-bridge
cp ./sequential-thinking-bridge ~/opt/sequential-thinking-bridge/sequential-thinking-bridge
chmod +x ~/opt/sequential-thinking-bridge/sequential-thinking-bridge
```

创建 `~/Library/LaunchAgents/com.repairman.sequential-thinking-bridge.plist`：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.repairman.sequential-thinking-bridge</string>

  <key>ProgramArguments</key>
  <array>
    <string>/Users/repairman/opt/sequential-thinking-bridge/sequential-thinking-bridge</string>
    <string>serve</string>
    <string>--listen</string>
    <string>127.0.0.1:38989</string>
    <string>--path</string>
    <string>/mcp</string>
    <string>--session-ttl</string>
    <string>2h</string>
  </array>

  <key>EnvironmentVariables</key>
  <dict>
    <key>DISABLE_THOUGHT_LOGGING</key>
    <string>true</string>
  </dict>

  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>

  <key>StandardOutPath</key>
  <string>/Users/repairman/opt/sequential-thinking-bridge/var/stdout.log</string>
  <key>StandardErrorPath</key>
  <string>/Users/repairman/opt/sequential-thinking-bridge/var/stderr.log</string>
</dict>
</plist>
```

加载并启动：

```bash
mkdir -p ~/opt/sequential-thinking-bridge/var
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.repairman.sequential-thinking-bridge.plist
launchctl enable "gui/$(id -u)/com.repairman.sequential-thinking-bridge"
launchctl kickstart -k "gui/$(id -u)/com.repairman.sequential-thinking-bridge"
```

查看状态：

```bash
launchctl print "gui/$(id -u)/com.repairman.sequential-thinking-bridge"
curl -fsS http://127.0.0.1:38989/healthz
```

重启服务：

```bash
launchctl kickstart -k "gui/$(id -u)/com.repairman.sequential-thinking-bridge"
```

卸载服务：

```bash
launchctl bootout "gui/$(id -u)" ~/Library/LaunchAgents/com.repairman.sequential-thinking-bridge.plist
```

## Codex MCP 配置示例

未加 token：

```toml
[mcp_servers.sequential-thinking-bridge]
type = "http"
url = "http://127.0.0.1:38989/mcp"
```

加 token：

```toml
[mcp_servers.sequential-thinking-bridge]
type = "http"
url = "http://127.0.0.1:38989/mcp"
http_headers = { "Authorization" = "Bearer <token>" }
```

## 验证重点

1. `initialize` 返回 `Mcp-Session-Id`。
2. 后续 `tools/call` 带同一个 `Mcp-Session-Id` 时，`thoughtHistoryLength` 连续增长。
3. 不同 `Mcp-Session-Id` 的 `thoughtHistoryLength` 互不影响。
4. RSS 应明显低于多实例 Node 方案。

## 上游对标基线

- 上游仓库：https://github.com/modelcontextprotocol/servers
- 上游路径：`src/sequentialthinking`
- 当前对标 commit：`7b1170d1da1e36bc9f553f51e76e64cbfd652b3e`
- commit 日期：`2026-06-16T18:40:51-07:00`
- commit 标题：`feat(memory): expose knowledge graph as MCP Resource (#3323)`
- 机器可读基线：`upstream-baseline.json`
- 限定同步文件：`index.ts`、`lib.ts`、`README.md`、`package.json`、`__tests__/lib.test.ts`
- 同步规则：工具 `description`、schema、annotations、README 等机械内容可同步；`lib.ts` 核心逻辑变化必须先人工 review。

## 兼容边界

- 成功路径按上游对标基线的 `src/sequentialthinking` 对齐，包括长 description、布尔字符串大小写兼容、分支插入顺序和默认 thought 日志。
- 失败路径不是字节级等价：原版通过 MCP SDK + Zod 产生校验错误；Go 版用手写 JSON-RPC 错误返回相同类别的失败。
