# 首版验证记录

2026-11-05，Go 1.26.5，PostgreSQL 17 + pgvector 0.8.0，官方 MCP Go SDK v1.5.0。

- `go vet ./...` 通过。
- 在真实 PostgreSQL 上运行 `TEST_DATABASE_URL=... go test -race -count=1 ./...` 通过，数据库测试未跳过。
- 业务测试覆盖完整闭环、Source 内容不可变、附件禁止事后追加、固定历史证据、冲突评估共存、跨 Workspace 引用和检索隔离。
- 并发测试覆盖相同幂等键的重复提交、expected_version 冲突、复审与新证据竞争、任务独占领取、过期租约恢复及旧领取者拒绝写回。
- 检索测试使用模拟 HTTP Embedding 服务，覆盖中文子串、标签、未配置时降级、索引滞后、旧任务、模型/维度切换、API 失败和有限重试。
- MCP 实际 HTTP 测试覆盖 SSE 2024-11-05 与 Streamable HTTP 2025-11-25 的初始化、发现、写入、检索、图片读取、断开重连、自定义路径、Origin、会话调用者隔离和撤销密钥。
- REST multipart 上传、二进制下载、只读密钥验证通过。
- OpenAPI 重新生成与仓库文件一致。

Embedding 使用模拟服务验证接口和失败路径，未调用付费模型接口。首版向量检索采用精确扫描，大规模语料的性能和多实例 MCP 会话路由未做容量验证。

独立二进制部署取代原容器交付。独立运行 CLI、API 服务、healthcheck 和 `scripts/agent-example.py` 通过；示例成功创建 Knowledge 版本 2、读取版本 1，并保留相反评估。

独立部署验证：`./scripts/build.sh` 生成 bin/knowledgehub，file/ldd 确认静态链接。使用直接运行的 PostgreSQL 17.11 + pgvector 0.8.0 执行 `./scripts/test-integration.sh`，全项目 race 测试通过。将单个二进制复制到临时目录，不携带源码、配置或初始化文件，通过环境变量及预装扩展的普通数据库角色执行初始化、CLI、serve、healthcheck 和完整 Agent 示例，均通过。

可选 pgvector 验证：数据库仅做新库初始化，初始化入口为 `init-db`，不保存结构版本、不升级旧库。插件已启用与插件控制文件完全不可用两种独立 PostgreSQL 环境下，完整 race 测试均通过（无插件时仅向量专用测试跳过）。无插件时的关键词/混合检索、显式 semantic 错误、无索引任务及零 Embedding 调用均有集成测试。最终二进制在全新无 pgvector 数据库上通过 init-db、启动、健康检查及 Agent 闭环示例。
