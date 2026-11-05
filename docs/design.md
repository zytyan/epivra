# Source / Knowledge 最小设计

## 数据模型

Source 保存不可变的正文、来源描述、来源时间、标签、元数据、作者、接收时间及附件。正文可以是客户端导入的会话片段、日志摘要或用户提供的材料；服务不抓取外部 URL。附件保存为 bytea，带 MIME、大小和 SHA-256。来源及其附件不能在提交后被更新或追加，修正材料应新建 Source。

Knowledge 使用稳定 UUID 和 current_revision 指针；不可变修订记录保存正文、JSONB scope、适用条件、标签、Source 关联、作者、时间及修订原因。正文上限 2000 个 Unicode 字符，由客户端负责使内容简短、原子化。每次保存完整快照，至少引用一个同 Workspace 的 Source。

没有 Claim、Verification、Assessment、Review，也没有知识分支、状态评估或后台任务对象。Workspace、API Key、附件、来源关联、修订和幂等记录是必要的存储及授权结构。

## Scope 与软件版本

scope 是开放 JSON 对象，空对象 `{}` 表示没有声明结构化限制。客户端可以描述 product、software.version、branch、build、hardware 等条件，文字补充写入 conditions。不同软件版本的独立事实应使用不同 Knowledge ID；即使 scope 相同，也允许多个原子知识并存。

Knowledge 的修订序号完全独立于软件版本。服务只对明确的 ID 追加修订，不选择“最新版知识”，不按版本号覆盖，不自动把旧软件知识判为过期。旧版本仍可以新增事实或修订现有事实。

scope 包含在每次不可变修订内。纠正 scope 必须显式修订并给出原因，历史仍返回原 scope；需要并存时应新建 ID。数据库不对 scope 建立唯一约束。

搜索只检索每个 ID 的当前修订，按 `stored_scope @> requested_scope` 过滤。无过滤时并列检索所有 scope，不隐含“最新软件版本”。过滤不自动包含 scope={} 的知识，也不求解版本范围；调用者可单独查询通用知识后自行判断适用性。

## 一致性与历史

所有关联带 Workspace 复合外键，跨 Workspace Source 引用被数据库拒绝。Source、Knowledge 修订及来源关联由数据库触发器保护，不允许修改已经提交的证据快照。

修订要求 expected_revision，事务内锁定指定 Knowledge，比较当前修订，追加完整新修订及来源关联，然后更新当前指针。冲突返回 revision_conflict 和 current_revision。Source/Knowledge 保存支持 Workspace、API Key、save 操作范围内的幂等键；相同参数重试返回原结果，不同参数返回 idempotency_conflict。

read 可以读取指定修订及截至该修订的历史；正文、scope、原因及来源 ID 都固定到该修订。不会把当前 Source 关联替换进旧修订。读操作使用数据库一致快照。

## MCP 与 Dream

`/mcp` 只注册 search、read；`/mcp/write` 额外注册 save。客户端决定每个会话可见的端点，正常工作只连接 `/mcp`。Dream 使用独立的可写会话和写入 Key，仍受 Workspace 权限检查。

客户端维护历史分析进度：读取本地新增历史，检索适用 scope 的 Knowledge，读取候选完整内容和历史，再决定新增、修订或忽略。客户端负责 LLM、Dream 调度、去重和会话上下文。服务端只执行明确请求，不自动生成或裁决知识。

search 的 since 仅是按 Source 接收时间或 Knowledge 当前修订时间的辅助过滤，不是可靠事件队列。需要增量扫描时由客户端保存进度、使用重叠时间窗口并按 ID/修订去重；本地历史是 Dream 的输入依据。

## 交付边界

一个独立 Go 二进制连接独立 PostgreSQL，使用 pg_trgm 和全文/子串关键词检索；不需要 pgvector，不调用 Embedding 或其他模型 API。没有 worker、任务表或额外基础设施。

测试期只初始化新库，不迁移或兼容旧结构。修改结构时重建测试库；应用不自动删除数据。未来是否增加其他能力，由用户明确决定。
