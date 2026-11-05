# 三个操作

MCP 工具和 `POST /v1/{search|read|save}` 使用相同 JSON 参数。所有调用要求 workspace_id 和 Bearer API Key。未知字段、显式顶层 null、旧接口参数均拒绝；可选字段请省略。

成功返回 `{"data":...,"warnings":[],"page":...}`；错误为 `{"error":{"code":"revision_conflict","message":"...","details":{"current_revision":2}}}`。HTTP 401/403 表示认证/权限失败，404 表示不存在，409 表示修订或幂等冲突，413 表示上传超限，其他输入错误为 400。

## save

保存不可变 Source：

```json
{"workspace_id":"UUID","type":"source","body":"会话中的原始观察……","provenance":"session-42, messages 10-15","idempotency_key":"session-42:10-15"}
```

可选 source_time（RFC3339）、metadata（JSON 对象）、tags（字符串数组）、attachments。Source 不支持修改，不传 id 或 expected_revision。

新增 Knowledge：

```json
{"workspace_id":"UUID","type":"knowledge","body":"启用选项 X 后需要重新初始化缓存。","scope":{"product":"example","software":{"version":"2.4","branch":"stable"}},"conditions":"启用 X 后首次启动","tags":["缓存"],"sources":["SOURCE_UUID"],"reason":"Dream 从新日志提取","idempotency_key":"dream-8:fact-1"}
```

scope 必须是对象，可显式设为 `{}`；至少一个 Source ID，reason 必填。修订时额外传 id 和 expected_revision，仍提交完整正文、scope、conditions、tags、sources 和 reason；省略可选条件/标签表示清空，不合并旧快照。正文、conditions 和 reason 各最长 2000 字符，scope 最长 64 KiB，最多引用 100 个 Source。不同软件版本需要并存时用新 ID，服务不自动合并。

结果 data 是 `{"type":"knowledge","id":"UUID","revision":1}` 或 Source 的 `{type,id}`。revision 是知识修订，软件版本只存在 scope 中。

幂等键长 1..200 字节。同一 Workspace/API Key 下，相同 save 参数可安全重试。发生 revision_conflict 时先读取当前版本，再由客户端判断是否继续修订，不能仅替换 expected_revision 盲目重试。

## search

```json
{"workspace_id":"UUID","query":"初始化缓存","types":["knowledge"],"scope":{"software":{"version":"2.4"}},"tags":["缓存"],"limit":20,"offset":0}
```

types 默认 `["knowledge"]`，可含 source；scope 使用 JSONB @> 包含过滤，tags 为 AND。无 scope 过滤时包含全部软件版本；结果按文本相关度排序、ID 打破平局，不按版本或时间推断正确性。Source 没有 scope，非空 scope 过滤会排除 Source。空 query 可用于按过滤条件列举。

命中返回 type、id、revision（Source 为 0）、snippet、scope 和 score。关键词支持中文片段。since（RFC3339）可过滤此时间之后接收的 Source 或产生当前修订的 Knowledge，详见设计中的增量扫描限制。

limit 默认 20、范围 1..100；offset 默认 0、最大 100000。page 包含 limit、offset、has_more。

## read

```json
{"workspace_id":"UUID","type":"knowledge","id":"UUID","revision":1,"history":true,"limit":20,"offset":0}
```

revision 省略/0 表示当前知识修订；history=true 返回截至所选修订的历史，按修订倒序排列。Source 读取不传 revision/history。

data.object 是完整 Source 或 Knowledge 快照，Knowledge 包含固定来源 ID 数组。data.sources 是对应修订的来源清单（ID、来源描述、接收时间），附件清单也只来自该修订。sources_page、attachments_page、history_page 分别给出各列表分页；同一 limit/offset 应用于所有列表。要获取证据正文，显式 read 对应 Source ID。

读取附件先 read Source/Knowledge 得到清单，然后：

```json
{"workspace_id":"UUID","type":"source","id":"SOURCE_UUID","attachment_id":"ATTACHMENT_UUID"}
```

结果包含附件的 name、mime、size、sha256 和 base64 data；MCP 对 PNG/JPEG/WebP/GIF 返回 image content，text 中保留附件元信息。不能通过另一个 Source ID 读取该附件。

上传在 save Source 中提供 `attachments:[{"name":"log.txt","mime":"text/plain","data":"BASE64"}]`。一次 Source 最多 100 个附件、总大小默认 20 MiB，由 MAX_UPLOAD_BYTES 配置；Source 正文最大 4 MiB。图片等附件需要提供必要的文字描述，服务不执行 OCR。
