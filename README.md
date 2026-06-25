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
