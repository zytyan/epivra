# 首版验证记录

2026-11-05，Go 1.26.5，PostgreSQL 17 + pgvector 0.8.0，官方 MCP Go SDK v1.5.0。

- `go vet ./...` 通过。
- 在真实 PostgreSQL 上运行 `TEST_DATABASE_URL=... go test -race -count=1 ./...` 通过，数据库测试未跳过。
- 业务测试覆盖完整闭环、Source 内容不可变、附件禁止事后追加、固定历史证据、冲突评估共存、跨 Workspace 引用和检索隔离。
- 并发测试覆盖相同幂等键的重复提交、expected_version 冲突、复审与新证据竞争、任务独占领取、过期租约恢复及旧领取者拒绝写回。
- 检索测试使用模拟 HTTP Embedding 服务，覆盖中文子串、标签、未配置时降级、索引滞后、旧任务、模型/维度切换、API 失败和有限重试。
- MCP 实际 HTTP 测试覆盖 SSE 2024-11-05 与 Streamable HTTP 2025-11-25 的初始化、发现、写入、检索、图片读取、断开重连、自定义路径、Origin、会话调用者隔离和撤销密钥。
- REST multipart 上传、二进制下载、只读密钥验证通过。
- OpenAPI 重新生成与仓库文件一致，Compose 配置校验通过。

Embedding 使用模拟服务验证接口和失败路径，未调用付费模型接口。首版向量检索采用精确扫描，大规模语料的性能和多实例 MCP 会话路由未做容量验证。

Docker 镜像构建通过。独立运行 CLI、API 服务、healthcheck 和 `scripts/agent-example.py` 通过；示例成功创建 Knowledge 版本 2、读取版本 1，并保留相反评估。
