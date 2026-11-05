# Epivra · Knowledge Hub

Go + PostgreSQL 的 Agent 知识库：Source → Claim → Verification → Assessment → Knowledge。REST 与 MCP 使用同一个业务服务；判断、提取和修订由 Agent 完成，服务保存证据链、历史、权限、并发版本和待复审事件。

## 启动

需要 Go 1.25+ 编译；运行时只需二进制和独立部署的 PostgreSQL（16/17），数据库需安装 pgvector 0.8.0+ 和 pg_trgm。数据库迁移已嵌入二进制，无需携带源码或迁移文件。

```sh
./scripts/build.sh
# 也可以直接编译：
CGO_ENABLED=0 go build -mod=readonly -trimpath -o bin/knowledgehub ./cmd/knowledgehub

export DATABASE_URL='postgres://knowledgehub:your_password@127.0.0.1:5432/knowledgehub?sslmode=disable'
export LISTEN_ADDR=127.0.0.1:8080
./bin/knowledgehub migrate
./bin/knowledgehub workspace-create --name research
# 将上一步 workspace_id 填入下一条命令。
./bin/knowledgehub key-issue --workspace WORKSPACE_UUID --name agent-1 --write
./bin/knowledgehub serve
```

编译产物是 `bin/knowledgehub`，可单独复制到相同操作系统/架构的机器运行，不依赖 Go、C 运行库或 PostgreSQL 客户端。交叉编译示例：`GOOS=linux GOARCH=arm64 ./scripts/build.sh`。所有运行配置通过环境变量传入，见 [.env.example](.env.example)；程序不自动读取 .env。需要从文件载入时，在可信的 shell 环境文件中填写配置后运行 `set -a; . ./.env; set +a`。

PostgreSQL 的独立部署、数据库权限及完整环境变量说明见 [部署文档](docs/deployment.md)。`serve` 启动时也会自动执行迁移；建议首次上线先用 `migrate` 完成数据库准备。

API Key 只在发放时输出一次，数据库保存 SHA-256 摘要。每个 Agent 单独发放；不加 `--write` 得到只读密钥。撤销：

```sh
./bin/knowledgehub key-revoke --id KEY_UUID
./bin/knowledgehub healthcheck
```

示例配置监听 `127.0.0.1:8080`；省略 LISTEN_ADDR 时监听 `:8080`。`/healthz` 检查进程，`/readyz` 检查数据库及迁移。远程访问时在反向代理终止 TLS，并设置 `PUBLIC_BASE_URL`、`TRUSTED_ORIGINS`；SSE 代理应关闭响应缓冲。附件也在数据库中，备份 PostgreSQL 即包含所有业务数据。

## 接口

所有业务调用携带 `Authorization: Bearer API_KEY` 和 `workspace_id`。每次写入要求 `idempotency_key`；同一 Workspace、API Key 和操作下，相同键和参数返回原结果，不同参数返回 `idempotency_conflict`。

| MCP 工具 | REST POST 路径 | 用途 |
|---|---|---|
| `source_create` | `/v1/sources` | 保存不可变文本、来源和附件 |
| `source_tags_update` | `/v1/sources/tags` | 带版本和原因替换标签 |
| `claim_create` | `/v1/claims` | 从至少一个 Source 创建 Claim |
| `claim_revise` | `/v1/claims/revisions` | 创建不可变 Claim 修订 |
| `verification_create` | `/v1/verifications` | 验证明确的 Claim 修订，可原子创建输出 Source |
| `assessment_create` | `/v1/assessments` | 综合同一 Claim 修订的 Verification |
| `knowledge_publish` | `/v1/knowledge` | 创建或修订 Knowledge |
| `knowledge_read` | `/v1/read` | 通用对象读取及固定版本证据展开 |
| `knowledge_history` | `/v1/history` | 修订原因、验证和评估历史 |
| `knowledge_search` | `/v1/search` | 关键词、语义、混合及待复审过滤 |
| `attachment_read` | `/v1/operations/attachment_read` | 按 ID 读取附件 |

所有工具也可经 `POST /v1/operations/{工具名}` 调用。方案未保留原十个工具的逐项名称，因此本实现将 Knowledge 创建和修订统一为 `knowledge_publish`，另补 `source_tags_update`，共 11 个工具。

详细契约见 [OpenAPI](docs/openapi.json)、[API 与一致性约定](docs/api.md)、[MCP 连接示例](docs/mcp.md) 和 [Agent 完整闭环](docs/agent-workflow.md)。`go run ./cmd/openapi > docs/openapi.json` 从 MCP 共用的输入结构生成 OpenAPI。

## 存储与复审

- 所有对象、修订、关系、附件和复审记录都受 Workspace 复合外键约束。Source 文本和附件不能修改或删除；标签变化产生新修订。Claim、Knowledge 具有稳定 ID。
- `claim_revise`、`source_tags_update`、带 `id` 的 `knowledge_publish` 必须携带 `expected_version` 和 `reason`。版本冲突返回当前版本，禁止静默覆盖。
- Verification 和 Assessment 只追加、固定到 Claim 的具体版本。Assessment 可以支持、反驳或不确定；不同结论并存。发布 Knowledge 必须至少引用一个支持评估，并明确关联该评估针对的 Claim 修订。
- 新验证、评估、Claim 修订生成事件并关联直接依赖的 Knowledge。Agent 用 `resolve_events` 明确声明已处理的事件；新到达的事件不会被批量清除。Assessment 的确认针对该 Claim 的事件，Knowledge 的确认仅关闭该 Knowledge 的待复审项。
- 首次发布也会检查已经到达、未纳入固定证据链且未经 Assessment 确认的事件。没有关联的新 Source 由检索发现，不推断影响范围。

## 检索与索引

默认 `hybrid`，支持 `keyword`、`semantic`。全文、trigram 和子串匹配支持中文片段；标签过滤要求包含所有给定标签。语义覆盖 Source、Claim、Knowledge 当前修订的正文及适用条件，按 Unicode 字符分块（800 字符，重叠 100），不执行抓取或 OCR；图片说明应写入 Source 正文。

通过环境变量设置 `EMBEDDING_BASE_URL`（包含服务的 `/v1`）、`EMBEDDING_API_KEY`、`EMBEDDING_MODEL`、`EMBEDDING_DIMENSIONS`。接口为 OpenAI 兼容的 `/embeddings`；配置见 [.env.example](.env.example)。不配置时关键词仍可用；显式语义请求返回 `semantic_unavailable`，混合查询返回关键词结果及降级警告。

模型、维度、端点或分块配置变化会改变指纹，重启后为所有当前修订入队重建。不同指纹的向量不混用。查询未完成索引时返回 `index_incomplete`。混合检索以 RRF（常数 60）合并候选排名。首版向量距离精确扫描，未增加需要固定维度的 ANN 索引。

worker 用 `SKIP LOCKED`、两分钟租约、30 秒续租、独立令牌和最多 5 次尝试领取任务；Embedding 请求不持有业务写事务。写回时再次检查租约、当前版本和配置。失败后按指数延迟重试，耗尽重试的任务保留为 `failed`，不会声称索引完整。管理员可在修复依赖后执行 `UPDATE index_jobs SET state='pending',attempts=0,available_at=now(),lease_until=NULL,lease_token=NULL WHERE state='failed'` 重试。

## 开发与测试

需要 Go 1.25+；CI 使用 Go 1.26.5。官方 MCP Go SDK 锁定 v1.5.0，兼容测试基线是 SSE `2024-11-05` 和 Streamable HTTP `2025-11-25`。

```sh
go mod download
go vet ./...
go test -race ./...
# 上述命令在没有 TEST_DATABASE_URL 时会显式跳过数据库集成测试。
# 对单独部署的专用测试数据库运行完整集成测试：
TEST_DATABASE_URL='postgres://test:test@localhost:5432/knowledgehub_test?sslmode=disable' ./scripts/test-integration.sh
```

测试为每个用例创建独立 schema，要求创建 schema/extension 的权限；需要 pgvector 和 pg_trgm。覆盖数据库不可变约束、证据固定、冲突评估、权限/跨 Workspace、幂等/CAS 并发、复审与新事件竞争、租约恢复、模型切换、中文检索、失败降级，以及两种 MCP 协议的实际 HTTP 初始化、发现、写入、读取和重新连接。

首版默认单实例。业务存储和任务领取可扩展，但 MCP 会话保存在进程内；进程退出后客户端必须重新初始化，不提供跨实例会话恢复。没有 Document、管理 UI、Redis、对象存储或自动模型评判。
