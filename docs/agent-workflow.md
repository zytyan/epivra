# 客户端 Dream 会话

默认空间自动可用，无需认证。正常工作可只向 Agent 暴露 `/mcp`，客户端保存本地会话历史。Dream 的调度、LLM、输入窗口和历史游标都由客户端管理，服务不自动安排整理任务。

一次增量 Dream：

1. 客户端加载尚未分析的本地历史，在独立上下文中连接 `/mcp/write`。
2. search 适用 scope 下的已有 Knowledge；必要时另查 scope={} 的通用知识，自行判断是否适用。跨版本比较需显式查询，不推断新软件知识更正确。
3. read 候选知识的全文、来源和历史。若已被完整覆盖则忽略，不写入。
4. 对有价值的新观察，save Source，保存对应历史片段和来源位置。
5. 新原子事实或不同软件版本的独立事实：save Knowledge，不带 id。修正一个已存在的事实：save 指定 id、expected_revision、完整 scope/来源及 reason。
6. 遇到 revision_conflict，重新读取，再判断新增、修订或忽略；不能盲目覆盖。网络失败使用同一幂等键和参数重试。
7. 客户端保存处理进度，结束 Dream 会话；下一次普通会话仍只连接 `/mcp`。

服务不自动选择修订对象、不去重、不给评估结论，也不记录 Dream 的执行阶段。search.since 仅供辅助筛选，不能替代客户端历史游标。

可执行的 REST 示例（与 MCP 调用相同业务逻辑）验证 Source→Knowledge 闭环、不同软件版本并存，以及旧软件 Knowledge 的独立修订：

```sh
export BASE_URL=http://localhost:8080
python3 scripts/agent-example.py
```

示例会新增测试数据；它演示明确操作，不调用模型，也不模拟自动正确性判断。
