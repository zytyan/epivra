# Epivra · Knowledge Hub

面向 Agent 的小型知识库，使用 Go + 独立 PostgreSQL，只保留 **Source** 和 **Knowledge**。

Source 保存不可变的历史记录、证据文本及可选附件。Knowledge 是不超过 2000 字符的原子知识，带 JSONB scope、必要条件、Source 引用、修订原因和独立历史。服务不调用模型，不执行 Dream、不做自动判断或知识覆盖。

## 构建与启动

需要 Go 1.25+ 编译，PostgreSQL 16/17 独立部署，安装标准 contrib 扩展 pg_trgm。不需要 pgvector、模型 API、任务队列或容器。

```sh
./scripts/build.sh
export DATABASE_URL='postgres://knowledgehub:password@127.0.0.1:5432/knowledgehub?sslmode=disable'
export LISTEN_ADDR=127.0.0.1:8080
./bin/knowledgehub init-db
./bin/knowledgehub workspace-create --name research
./bin/knowledgehub key-issue --workspace WORKSPACE_UUID --name reader
./bin/knowledgehub key-issue --workspace WORKSPACE_UUID --name dream --write
./bin/knowledgehub serve
```

产物 `bin/knowledgehub` 是静态二进制，SQL 初始化文件已嵌入，可单独复制运行。也可直接编译：`CGO_ENABLED=0 go build -mod=readonly -trimpath -o bin/knowledgehub ./cmd/knowledgehub`。交叉编译：`GOOS=linux GOARCH=arm64 ./scripts/build.sh`。

配置全部经环境变量传入，见 [.env.example](.env.example)。程序不自动加载 .env。`serve` 会初始化空库；`init-db` 可提前完成初始化。当前处于测试阶段，无旧结构兼容或升级迁移，需要重建原测试库，程序不会自动清空已有数据。见 [Agent.md](Agent.md)。

## MCP

| 默认端点 | 工具 | 客户端用途 |
|---|---|---|
| `/mcp` | `search`、`read` | 正常工作，只读取知识 |
| `/mcp/write` | `search`、`read`、`save` | 独立 Dream 会话整理知识 |

使用 Streamable HTTP（2025-11-25），所有请求携带 `Authorization: Bearer API_KEY`。客户端只在 Dream 会话暴露写入端点。即使使用写入 Key，`/mcp` 也不会列出或接受 `save`；`/mcp/write` 的保存仍要求 Workspace 写权限。两个端点的 MCP 会话互不通用。

REST 同样只提供 `POST /v1/search`、`POST /v1/read`、`POST /v1/save`，与 MCP 使用相同参数和业务逻辑。

## 软件版本与修订

```json
{
  "product": "example",
  "software": {"version": "2.4", "branch": "stable", "build": "abc123"},
  "hardware": {"architecture": "arm64", "device": "board-a"}
}
```

scope 是调用者定义的 JSON 对象，服务不解析版本号大小、分支关系或适用优先级。同一主题在不同软件版本中可以使用不同 Knowledge ID 并存、独立修订；旧软件也可以不断新增 Knowledge。`revision: 3` 只表示某个 Knowledge 的第 3 次修订，与 `scope.software.version` 没有数值或顺序关系。

保存时只修订调用者明确指定的 ID；不按 scope、正文相似度或软件版本自动合并。新建 Knowledge 不传 ID。搜索默认覆盖所有 scope 下的当前修订，只按文本相关度排序，不根据软件版本或时间判断正确性。按 scope 过滤采用 PostgreSQL JSONB 包含运算 `@>`，不做 SemVer 区间计算。

## 客户端 Dream

正常会话使用 `/mcp`，历史记录及进度由客户端保管。Dream 使用独立会话和 `/mcp/write`，增量分析历史、检索已有知识，随后选择新增、修订或忽略。新增证据用 `save(type=source)`，保存知识用 `save(type=knowledge)`；没有有用变化则不写入。服务不记录 Dream 状态或游标，也不安排模型调用。

详见 [设计](docs/design.md)、[API](docs/api.md)、[MCP 连接](docs/mcp.md)、[部署](docs/deployment.md) 和 [客户端示例](docs/agent-workflow.md)。[OpenAPI](docs/openapi.json) 由 `go run ./cmd/openapi` 生成。

## 验证

```sh
go vet ./...
go test -race ./...
# 未设置 TEST_DATABASE_URL 时，数据库集成测试显式跳过。
TEST_DATABASE_URL='postgres://test:test@localhost:5432/knowledgehub_test?sslmode=disable' ./scripts/test-integration.sh
```

集成测试在专用数据库中创建独立 schema，需要 schema 创建权限和 pg_trgm；不创建数据库或依赖 pgvector。覆盖多 scope 并存、旧软件持续修订、固定历史证据、来源/附件不可变、幂等与并发冲突、Workspace 隔离、MCP 工具可见性和会话隔离。CI 使用独立 PostgreSQL，构建并上传 Linux amd64 二进制。
