# 独立二进制部署

应用和 PostgreSQL 分别部署。应用只通过 DATABASE_URL 连接数据库；不创建 PostgreSQL 进程、不管理数据库数据目录。编译时要求 Go 1.25+，运行时无需 Go 或 PostgreSQL 客户端；系统需要 CA 证书以访问 HTTPS Embedding 服务或使用数据库证书校验。

## PostgreSQL 准备

数据库服务器安装 PostgreSQL 16/17 和 pg_trgm。pgvector 0.8.0+ 是可选依赖，仅用于语义检索；安装方式见[官方安装说明](https://github.com/pgvector/pgvector/tree/v0.8.0#installation)，pg_trgm 通常随 PostgreSQL 的 contrib 包提供。扩展必须安装在数据库服务器上。

使用数据库管理员创建应用角色和数据库（密码请替换）：

```sql
CREATE ROLE knowledgehub LOGIN PASSWORD 'replace_me';
CREATE DATABASE knowledgehub OWNER knowledgehub;
```

连接 knowledgehub 数据库，由管理员执行：

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
GRANT USAGE, CREATE ON SCHEMA public TO knowledgehub;
```

没有 pgvector 时，上述准备即可运行全部知识闭环和关键词检索。无需配置 Embedding；即使已经配置，服务也会关闭语义索引，不发送 Embedding 请求。显式 semantic 查询返回 `semantic_unavailable`，hybrid 返回关键词结果和说明插件未启用的警告。

需要语义检索时，应在新测试库初始化之前由管理员执行：

```sql
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;
```

然后配置 EMBEDDING_BASE_URL 等变量，运行 `init-db` 或 `serve`。程序不会自动安装 pgvector，只在新库初始化时检测 public schema 中已启用的扩展，并决定是否创建向量表。

测试阶段没有版本迁移和旧库兼容逻辑。已有测试库修改结构或切换 pgvector 能力时，需要自行重建测试库再初始化；应用启动不会更新旧结构，也不会自动清空数据。

应用角色需要拥有其业务表，并有创建表、函数、触发器等初始化权限。管理员预先安装所需扩展后，应用角色不需要超级用户权限。默认业务对象创建在 search_path 的首个 schema；连接的 search_path 必须包含 public，以便解析扩展类型和函数。

## 构建与运行

```sh
./scripts/build.sh
export DATABASE_URL='postgres://knowledgehub:replace_me@db-host:5432/knowledgehub?sslmode=verify-full'
export LISTEN_ADDR=127.0.0.1:8080
./bin/knowledgehub init-db
./bin/knowledgehub serve
```

DATABASE_URL 中的密码若含 URL 保留字符，须进行 URL 编码。仅本机测试可使用 sslmode=disable；远程连接按数据库部署配置证书、sslmode、sslrootcert 等连接参数。CLI 和服务使用相同的环境变量。

所有初始化已嵌入程序；复制单个 bin/knowledgehub 即可部署。不加载相对目录中的配置文件。可用进程管理器注入环境变量，或从可信 shell 文件加载 `.env.example` 的副本后启动程序。标准输出用于 CLI 结果，结构化日志写入标准错误。SIGTERM/SIGINT 执行正常关闭。

## 环境变量

| 变量 | 默认值 | 用途 |
|---|---|---|
| DATABASE_URL | 必填 | 外部 PostgreSQL 连接串 |
| LISTEN_ADDR | :8080 | HTTP 监听地址；仅本机访问可设为 127.0.0.1:8080 |
| MAX_UPLOAD_BYTES | 20971520 | 单次 Source 附件总大小上限 |
| MCP_PATH | /mcp | Streamable HTTP 路径 |
| MCP_SSE_PATH | /sse | 旧版 SSE 路径 |
| MCP_MESSAGES_PATH | /messages | 旧版消息路径 |
| PUBLIC_BASE_URL | 空 | 对外 SSE 消息地址的基础 URL |
| TRUSTED_ORIGINS | 空 | 允许的精确 Origin，逗号分隔 |
| EMBEDDING_BASE_URL | 空 | 兼容接口根 URL（包括 /v1）；为空时关闭语义索引 |
| EMBEDDING_API_KEY | 空 | Embedding 服务密钥 |
| EMBEDDING_MODEL | text-embedding-3-small | 模型名 |
| EMBEDDING_DIMENSIONS | 1536 | 向量维度 |
| EMBEDDING_TIMEOUT | 30s | 单批请求超时 |
| EMBEDDING_BATCH_SIZE | 16 | 单批输入数 |

healthcheck 使用 LISTEN_ADDR 的端口访问本机 /readyz；可独立执行 `./bin/knowledgehub healthcheck`。数据库备份、TLS、可用性和升级由 PostgreSQL 的独立部署负责。
