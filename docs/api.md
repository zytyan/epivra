# API 与一致性约定

API 接受 JSON，未知字段和显式 null 拒绝；可选字段请省略。时间为 RFC3339，读取以 UTC 输出。成功返回 `{"data":...,"warnings":[],"page":{"limit":20,"offset":0,"has_more":false}}`，非列表操作省略 page。失败返回 `{"error":{"code":"version_conflict","message":"...","details":{"current_version":2}}}`。

| HTTP | 错误码 |
|---|---|
| 400 | `invalid_input`、`invalid_reference` |
| 401 | `unauthorized` |
| 403 | `forbidden` |
| 404 | `not_found`、`unknown_operation` |
| 409 | `version_conflict`、`idempotency_conflict`、`conflict` |
| 413 | `upload_too_large` |
| 500 | `internal` |
| 503 | `semantic_unavailable` |

`conflict` 表示数据库并发冲突，可用原幂等键重试。`version_conflict` 应先读取最新版本，再判断是否修订，并为新的请求选择新的幂等键。幂等性比较规范化的完整参数，数组顺序有意义；不要用同一键提交不同内容。

对象引用为 `{"type":"claim","id":"UUID","version":1}`。Source 引用可以省略 version，写入时解析并固定当时的当前版本；Claim、Verification、Assessment 历史依据必须提供 version。读取 ref 时省略 version 或设为 0 表示当前版本。写入结果 data 是新对象的 `{type,id,version}`。

`knowledge_publish` 不传 id 时创建；传 id 时必须传 expected_version、reason。每次修订是完整快照，必须重新提供正文、标签、适用条件和全部依据；不会合并旧字段。`source_tags_update` 的 tags 是替换数组，可为空数组。

`knowledge_read` 支持所有对象类型。data 包含 revision、links、attachments、evidence、assessments、review_events。默认 depth=0，返回正文、直接关系和固定证据链上的附件清单，不返回附件二进制。depth=1..5 展开固定版本证据。`assessments` 是对应 Claim 修订的全部评估，可包含发布后新增的相反评估；它们不是原发布依据，原依据始终由固定的 links/evidence 表示。

limit 默认 20、最大 100，offset 默认 0、最大 100000。外层 page 描述 evidence 分页；links_page、attachments_page、related_page、review_page 分别描述其他列表。相同 limit/offset 应用于每个列表，依据各自 has_more 继续翻页。历史按根对象选定版本返回其修订及相关验证和评估；后来新增的评估仍可查询，不会替换历史依据。

复审事件带 `id`、`claim_id`、`cause_id`、`cause_version`、`event_type` 和时间。读取 Knowledge 返回该对象未关闭事件；读取 Claim 返回未经 Assessment 确认的事件。Assessment 或 Knowledge 写入 `resolve_events:["EVENT_UUID"]` 只关闭这些事件。不能用其他 Workspace 或无关 Claim 的事件。确认依据保存在追加式 event_resolutions 中。

## 附件

MCP/JSON 使用 `attachments:[{"name":"result.txt","mime":"text/plain","data":"BASE64"}]`。默认一次 Source 的附件总大小限制为 20 MiB，通过 MAX_UPLOAD_BYTES 配置；正文另限 4 MiB。不自动抓取 provenance 中的 URL。

REST multipart 的 metadata 字段是完整 source_create 参数的 JSON，附件用重复的 attachments 字段：

```sh
curl -H "Authorization: Bearer $API_KEY" \
  -F "metadata={\"workspace_id\":\"$WORKSPACE_ID\",\"idempotency_key\":\"upload-1\",\"provenance\":\"local experiment\",\"source_type\":\"file\"}" \
  -F 'attachments=@result.txt;type=text/plain' \
  http://localhost:8080/v1/sources
```

附件清单含 ID、所属 Source、文件名、MIME、大小和 SHA-256。显式下载：`GET /v1/attachments/{id}?workspace_id=UUID`，同样要求 Bearer Key。响应使用 Content-Disposition attachment 和 SHA-256 ETag。`attachment_read` 对普通文件返回 data.base64 内容（字段名为 `data`），MCP 图片使用 image content。

## 检索

```json
{
  "workspace_id": "WORKSPACE_UUID",
  "query": "实验结果",
  "mode": "hybrid",
  "types": ["knowledge"],
  "tags": ["实验"],
  "pending_review": true,
  "limit": 20,
  "offset": 0
}
```

命中包含 ref（含版本）、snippet、methods、score、pending_review，语义命中带分块 position。只检索当前修订；历史通过 read/history 获取。空关键词可按条件列出对象；显式 semantic 要求非空 query。tags 是 AND，types 是 OR。RRF 使用每种方法前 `max(100,offset+limit+1)` 个候选，最终分页；它不保证并发写入期间跨多个请求的快照一致性。
