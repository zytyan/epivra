# 验证范围

当前模型仅 Source / Knowledge；MCP 只有 search、read、save。

自动集成测试覆盖：

- JSONB scope 嵌套过滤、不同软件版本知识并存、旧软件持续新增和修订。
- Knowledge 修订历史、原因和固定 Source 引用，分页读取历史及附件。
- Source/附件和提交后证据关联的数据库不可变约束。
- 同一幂等键并发重复请求、expected_revision 冲突、跨 Workspace 引用和检索拒绝。
- 原子知识长度、必要来源/原因/scope 校验及事务回滚。
- `/mcp` 仅 search/read，`/mcp/write` 额外 save；只读端点不能通过直接调用绕过可见性。
- API Key 权限、撤销、Origin 校验，调用者及端点之间的会话隔离。
- 完整 Source→Knowledge MCP 操作和断开后新会话读取。

验证使用独立 PostgreSQL，运行 `go vet ./...`、`go test -race -count=1 ./...`，并构建静态二进制。服务不依赖 pgvector 或模型 API。示例脚本测试新软件和旧软件的独立演化。

本轮验证：2026-11-05，Go 1.26.5 + 独立 PostgreSQL 17。全项目真实数据库 race 测试和 go vet 通过，OpenAPI 已重新生成。静态二进制在仅含程序的临时目录中，以普通数据库角色完成 init-db、serve、healthcheck 和 scripts/agent-example.py；示例确认旧软件知识修订到 2、新软件知识保持 1，以及旧修订来源不变。
