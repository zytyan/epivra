# MCP 连接

使用官方 Go SDK v1.5.0，Streamable HTTP 协议基线 2025-11-25。两个默认端点：

- `/mcp`：只读，发现和调用 search、read。
- `/mcp/write`：读写，发现和调用 search、read、save。

客户端在正常会话只配置 `/mcp`；整理时使用独立会话连接 `/mcp/write`。服务自动使用共享 default Workspace，两个端点均不需要认证或 workspace_id。`/mcp` 不能调用 save；访问 `/mcp/write` 的客户端均可保存。

初始化：

```sh
curl -i http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"agent","version":"1"}}}'
```

保存响应的 Mcp-Session-Id，后续请求携带同一头以及 `Mcp-Protocol-Version: 2025-11-25`。发送 notifications/initialized 后调用 tools/list 或 tools/call：

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search","arguments":{"query":"缓存","scope":{"software":{"version":"2.4"}}}}}
```

成功的业务 JSON 位于 result.content[0].text。业务错误返回 isError=true；不可见工具调用由 SDK 返回 JSON-RPC error。图片附件读取可追加 image content。

会话仅按端点隔离，不能把 `/mcp/write` 的会话带到 `/mcp`。会话 ID 不是用户身份。空闲会话 30 分钟过期，进程退出后不恢复，客户端重新初始化并按原幂等键重试不确定的保存。

`MCP_READ_PATH` 和 `MCP_WRITE_PATH` 可修改端点路径。`TRUSTED_ORIGINS` 指定逗号分隔的精确 Origin；默认拒绝带 Origin 的浏览器请求，普通 CLI 无 Origin 请求可用。
