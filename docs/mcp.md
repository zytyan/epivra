# MCP 连接

使用官方 Go SDK v1.5.0，Streamable HTTP 协议基线 2025-11-25。两个默认端点：

- `/mcp`：只读，发现和调用 search、read。
- `/mcp/write`：读写，发现和调用 search、read、save。

客户端在正常会话只配置 `/mcp`；Dream 使用独立会话连接 `/mcp/write`。不要把可写端点默认挂到正常工作会话。端点控制工具可见性；API Key 仍控制 Workspace 读写权限。只读 Key 即使连接 `/mcp/write` 也无法保存。写 Key 在 `/mcp` 也不能调用 save。

所有请求携带 `Authorization: Bearer API_KEY`。初始化：

```sh
curl -i http://localhost:8080/mcp \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"agent","version":"1"}}}'
```

保存响应的 Mcp-Session-Id，后续请求携带同一头以及 `Mcp-Protocol-Version: 2025-11-25`。发送 notifications/initialized 后调用 tools/list 或 tools/call：

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search","arguments":{"workspace_id":"UUID","query":"缓存","scope":{"software":{"version":"2.4"}}}}}
```

成功的业务 JSON 位于 result.content[0].text。业务错误返回 isError=true；不可见工具调用由 SDK 返回 JSON-RPC error。图片附件读取可追加 image content。

会话按端点和 API Key 隔离，不能把 `/mcp/write` 的会话带到 `/mcp`，也不能使用另一个 Key 重用会话。每个 HTTP 请求都检查密钥；撤销后既有会话的后续请求也被拒绝。空闲会话 30 分钟过期，进程退出后不恢复，客户端重新初始化并按原幂等键重试不确定的保存。

`MCP_READ_PATH` 和 `MCP_WRITE_PATH` 可修改端点路径。`TRUSTED_ORIGINS` 指定逗号分隔的精确 Origin；默认拒绝带 Origin 的浏览器请求，普通 CLI 无 Origin 请求可用。
