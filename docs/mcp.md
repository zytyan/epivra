# MCP 连接

服务使用[官方 Go SDK v1.5.0](https://github.com/modelcontextprotocol/go-sdk/tree/v1.5.0)，其[协议版本列表](https://github.com/modelcontextprotocol/go-sdk/blob/v1.5.0/mcp/shared.go)包含 2024-11-05 和 2025-11-25。双端点遵循[官方双协议兼容说明](https://github.com/modelcontextprotocol/modelcontextprotocol/blob/main/docs/specification/2025-03-26/basic/transports.mdx)。

| 传输 | 默认入口 | 兼容测试基线 |
|---|---|---|
| Streamable HTTP | `http://localhost:8080/mcp` | `2025-11-25` |
| 旧 SSE | GET `http://localhost:8080/sse`，随后使用 endpoint 事件给出的 POST URL | `2024-11-05` |

所有 HTTP 请求都必须携带 `Authorization: Bearer API_KEY`，包括 SSE 建连和 POST 消息。旧版 POST 默认是 `/messages?session_id=...`；客户端必须使用 endpoint 事件中的地址。Streamable HTTP 客户端保存 Mcp-Session-Id，后续请求附带该头及 Mcp-Protocol-Version。不同 API Key 不能重用另一调用者的会话；撤销 Key 后，既有会话后续调用也拒绝。

Streamable HTTP 初始化示例：

```sh
curl -i http://localhost:8080/mcp \
  -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"example-agent","version":"1"}}}'
```

获得会话头后发送 `notifications/initialized`，再调用 `tools/list`、`tools/call`。工具参数与 REST JSON 相同，例如：

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "knowledge_search",
    "arguments": {"workspace_id":"WORKSPACE_UUID","query":"实验","mode":"keyword"}
  }
}
```

成功结果位于 MCP `result.content[0].text`，其文本是统一业务结果的 JSON。业务错误以 `result.isError=true` 和包含 error 的 text content 返回；协议层错误由 SDK 使用 JSON-RPC error 返回。读取 PNG/JPEG/WebP/GIF 附件时追加 image content，text 只保留附件元信息；其他二进制按需返回 base64。

`MCP_PATH`、`MCP_SSE_PATH`、`MCP_MESSAGES_PATH` 可独立配置，不得重叠。`PUBLIC_BASE_URL` 决定 SSE 广播的外部消息地址（可包含代理前缀）；为空时使用相对路径。`TRUSTED_ORIGINS` 是逗号分隔的精确 Origin 列表，默认拒绝所有带 Origin 的请求，允许普通 CLI Agent 的无 Origin 请求。

Streamable HTTP 空闲会话 30 分钟过期。连接断开后可重新 initialize；业务幂等键可安全恢复不确定是否成功的写入。进程退出后会话不恢复，多实例部署需要 MCP 会话粘性路由；首版按单实例部署。
