# Agent 操作闭环

准备好 Workspace 与写入 API Key 后，可以运行真正调用 REST 的示例：

```sh
export BASE_URL=http://localhost:8080
export WORKSPACE_ID=WORKSPACE_UUID
export API_KEY=YOUR_API_KEY
python3 scripts/agent-example.py
```

脚本每次使用新的幂等键，会在指定 Workspace 中保留示例业务数据。流程是：

1. `source_create` 保存实验原始信息，说明来源和类型。
2. `claim_create` 从 Source 提取主张，标明适用条件。
3. `verification_create` 固定到 Claim 版本 1，并用 output_source 原子保存重复实验结果。
4. `assessment_create` 汇总同一 Claim 修订的 Verification，给出有条件的支持结论。
5. `knowledge_publish` 明确引用 Claim 版本 1 和支持 Assessment。
6. 新增反例 Source、反驳 Verification 与 Assessment，保留两种相反评估。
7. `knowledge_read` 读取复审事件；Agent 评判条件差异后，用 expected_version=1 发布 Knowledge 版本 2，明确列出已处理的 resolve_events。
8. 再次读取 Knowledge 版本 1，验证旧正文及固定证据链仍可追溯。

MCP 调用同名工具、使用相同 arguments，即可执行同一流程。对可重试的网络失败保留原幂等键及原参数，重新发送；对版本冲突先读取新版本，再决定如何修改，不能把 expected_version 自动替换后盲目重试。

待复审工作队列由 `knowledge_search` 的 `types:["knowledge"]`、`pending_review:true` 得到。逐条读取 review_events、相关 Assessment 和固定版本证据；列表超过 limit 时继续分页。Agent 的 Assessment 或 Knowledge 修订只携带实际处理的事件 ID。处理过程中再到达的事件保持未关闭。

未关联的 Source 通过定期 keyword/hybrid 检索发现。服务不把新 Source 自动关联到主张，也不使用“最后一次评估”覆盖已有判断。Claim 正文发生变化时先 `claim_revise`，再针对新修订进行验证和评估，最后显式修订 Knowledge 的依据；旧 Knowledge 仍指向原 Claim 修订。
