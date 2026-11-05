# 独立二进制部署

一个 Go 二进制连接独立部署的 PostgreSQL 16/17。编译要求 Go 1.25+；运行不需要 Go、PostgreSQL 客户端或 pgvector。服务没有模型配置和后台任务。

## 数据库准备

安装 PostgreSQL 和标准 contrib 扩展 pg_trgm。管理员创建角色和新测试库：

```sql
CREATE ROLE knowledgehub LOGIN PASSWORD 'replace_me';
CREATE DATABASE knowledgehub OWNER knowledgehub;
```

连接新库后预建扩展，并授予初始化权限：

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
GRANT USAGE, CREATE ON SCHEMA public TO knowledgehub;
```

应用角色需拥有业务表和创建函数、触发器等权限，无需超级用户。search_path 必须包含 public，以便使用 pg_trgm。所有对象都创建在当前 schema。

当前是可破坏性修改的测试阶段，已有旧结构时请重建专用测试库。没有升级迁移、旧表兼容或自动数据删除逻辑；初始化只用于新库，重复启动已初始化的新结构不会重置数据。

## 构建和启动

```sh
./scripts/build.sh
export DATABASE_URL='postgres://knowledgehub:replace_me@db-host:5432/knowledgehub?sslmode=verify-full'
export LISTEN_ADDR=127.0.0.1:8080
./bin/knowledgehub init-db
./bin/knowledgehub workspace-create --name research
./bin/knowledgehub key-issue --workspace WORKSPACE_UUID --name reader
./bin/knowledgehub key-issue --workspace WORKSPACE_UUID --name dream --write
./bin/knowledgehub serve
```

复制单个 bin/knowledgehub 即可运行，初始化 SQL 已嵌入。`serve` 也会初始化空库。远程数据库的 sslmode、证书和数据库备份由独立部署负责；密码中的 URL 保留字符需编码。本机测试可用 sslmode=disable。

## 环境变量

| 变量 | 默认 | 用途 |
|---|---|---|
| DATABASE_URL | 必填 | PostgreSQL 连接串 |
| LISTEN_ADDR | :8080 | HTTP 监听地址 |
| MCP_READ_PATH | /mcp | 只读 MCP |
| MCP_WRITE_PATH | /mcp/write | 读写 MCP |
| TRUSTED_ORIGINS | 空 | 可信浏览器 Origin，逗号分隔 |
| MAX_UPLOAD_BYTES | 20971520 | 单次 Source 附件总字节上限 |

程序只读取环境变量，不自动读取 .env。可由进程管理器注入，或从可信 shell 文件载入 .env.example 的副本。日志写入标准错误；密钥发放结果输出一次，数据库只存摘要。`key-revoke --id KEY_UUID` 撤销密钥。

`/healthz` 检查进程，`/readyz` 检查数据库和初始化标记。`./bin/knowledgehub healthcheck` 使用 LISTEN_ADDR 端口查询本机 readyz。SIGTERM/SIGINT 正常关闭服务。MCP 会话保存在进程内，重启后客户端重新初始化。
