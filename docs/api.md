# HTTP 协议

所有业务路径以 `/v1` 开头。请求体使用 JSON，素材上传直接使用 PNG 字节。

| 方法和路径 | 请求 | 结果 |
|---|---|---|
| PUT /scenes/{id} | name, expectedRevision, document | 完整场景，200 |
| GET /scenes/{id} | 无 | 完整场景，200 |
| POST /scenes/{id}/state | expectedRevision, state | 完整场景，200 |
| POST /scenes/{id}/snapshots | expectedRevision | 快照和素材闭包，201 |
| PUT /assets/{id} | PNG字节 | 素材及base64字节，201 |
| POST /assets/{id}/retire | 无 | state，200 |
| GET /snapshots/{id} | 无 | 快照，200 |
| POST /snapshots/{id}/revoke | 无 | state，200 |
| GET /snapshots/{id}/package | 无 | atlas-scene交换包，200 |
| POST /imports/{id} | name, package | 新场景，201 |
| POST /jobs | snapshotId, scale；请求头Idempotency-Key | 任务，202 |
| GET /jobs/{id} | 无 | 任务，200 |
| POST /jobs/{id}/cancel | 无 | 任务，200 |
| POST /jobs/{id}/retry | 无 | 任务，202 |
| GET /artifacts/{id} | 可选If-None-Match | PNG，200或304 |
| DELETE /artifacts/{id} | 无 | state，200 |
| POST /compare | left, right（结果ID） | 像素比较，200 |
| GET /status | 无 | 场景/素材/快照/任务计数和结果字节数 |
| GET /events | 无 | 最多500个有序领域事件 |

`GET /healthz` 不带 `/v1`，返回ready。它表明进程已打开并恢复状态，不包含单独的磁盘可写探测。任务响应省略固定输入的有效载荷，输入细节通过快照接口读取。

文档字段：width、height、background、layers。每个图层有id、kind、width、height、transform、opacity、fill；transform包括x、y、scaleX、scaleY、degrees。图层可以有clip（x/y/width/height），group使用children，polygon使用points，image使用assetId。solid颜料有color；linear还包括endColor、from、to。示例是可直接提交的完整请求。

幂等键长度1–80，使用英文字母、数字、下划线和连字符，首位不能是后两者。它在服务实例持久状态中全局生效，不随任务完成、取消、结果清理或进程重启失效。改换请求语义需使用新键。

服务接受相同内容的素材重复上传，退役素材不接受重新上传。交换包导出保留快照状态；导入会创建全新的active场景，原快照不作为目标服务中的快照记录落地。导入目标ID必须未使用；共享素材必须内容相同且仍可用。

## 可选的结构化诊断（v2）

错误默认仍是原有的 `{"code","error"}` 结构，状态码和 `error` 文本与历史版本一致，旧客户端无需改动。客户端可在请求中任选一种方式选择新协议：

- 请求头 `Accept: application/vnd.atlas.diagnostics.v2+json`
- 请求头 `Atlas-Diagnostics: v2`

选择后，凡是能定位到实际被检查内容的拒绝会返回 `Content-Type: application/vnd.atlas.diagnostics.v2+json`：

```json
{
  "protocolVersion": 2,
  "subject": {"kind": "document", "origin": "save", "contentDigest": "…", "sceneId": "emblem"},
  "diagnostics": [{
    "code": "polygon.bounds",
    "severity": "error",
    "message": "polygon point must lie inside the layer local bounds",
    "layer": {"path": "/layers/2/0", "id": "facet", "kind": "polygon",
              "ancestors": [{"path": "/layers/2", "id": "inset", "kind": "group"}]},
    "field": "points.0",
    "evidence": {"x": 500, "y": 90, "width": 100, "height": 100}
  }]
}
```

- `subject` 说明诊断对应的内容，而不是某个场景的最新版本。`contentDigest` 是提交文档的规范摘要；`kind` 为 `document` 时来自保存或导入的文档，为 `snapshot` 时额外带 `snapshotId`、`snapshotDigest`、`revision` 和 `scale`，为 `package` 时是包本身（`digest`、`format` 校验失败）。`kind` 为 `asset` 时 `contentDigest` 是上传字节的摘要。
- `layer.path` 是指向文档内图层的 JSON Pointer；`ancestors` 从外到内列出包含它的组，`field` 指到具体字段。编辑器据此定位需要调整的图层关系。
- 保存和包导入对相同文档给出相同的 `contentDigest` 和相同诊断（`subject.origin` 分别为 `save` 和 `import`）；`subject` 不会被场景之后的编辑改变。
- 渲染成本拒绝（429）附带 `render.workBudget`：`evidence.cost` 是全部可绘制图层累加的图层像素，`evidence.budget` 是上限，`evidence.contributions` 按贡献降序列出最多八个图层及其 `pixels`，`evidence.drawableLayers`/`shown` 说明总数与列出数。它解释为什么单独合法的图层组合后超限，且该请求不会留下排队任务。
- 只有确实检查了内容时才返回 v2。无法绑定内容的错误（如 JSON 语法错误、实体不存在、幂等键冲突）即使客户端选择了 v2，仍返回原有 `{"code","error"}`。旧状态文件不需要迁移，也不会被改写。

