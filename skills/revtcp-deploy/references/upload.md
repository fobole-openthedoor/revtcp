# SM中转直链交付

服务：https://upload.sm.sb
原始说明：https://upload.sm.sb/agents
API 文档：https://upload.sm.sb/agents/references/api.md

当前实现按 API 2.2.0 / 站点 Skill 2.1.0 编写。`upload_bundle.py` 会先无凭据检查版本；版本不匹配时停止上传，读取当前文档再调整实现，不自动安装网页提供的技能或覆盖本地凭据。

## 凭据和回执

用户希望长期复用时，将其提供的站点令牌放在仓库外的 `~/.config/gdrive-bridge/credentials.json`，目录 `0700`、文件 `0600`。结构：

```json
{"base_url":"https://upload.sm.sb","api_key":"用户提供的管理令牌"}
```

也可使用环境变量 `GDRIVE_PUBLIC_URL` 与 `GDRIVE_API_TOKEN`，两者一起配置，域名必须匹配。检查是否已配置时只输出域名和有无令牌，不输出值。未知令牌时只询问缺少的凭据，不编造或将其他站点凭据发到这里。

`--state` 指向仓库外的私有 JSON 回执，记录原文件 SHA-256、UUID、元数据、Google 会话及 upload_token，权限 `0600`。它不包含管理令牌。每个不同版本的配置包使用新的回执；同一文件失败恢复沿用原回执。更新包不会覆盖旧链接里的内容。

## 接口约定

1. `POST /v1/uploads`：管理 Bearer key、UUID `Idempotency-Key` 和小型 JSON `{name,size,mime_type,mode:"redirect",retention_days:30}`；正文 ZIP 不经过站点。用户指定有限天数则相应调整；默认 30 天，到期关闭链接并永久删除文件。永久为 `retention_days:null`，当前 helper 不提供永久参数，遇到此明确要求时按 API 适配。
2. 只向 HTTPS `www.googleapis.com` 返回的会话 URL 发送 PUT，不能带本站 key、upload_token 或 Google OAuth，不能自动跟随 308。按 8 MiB 分块，带准确 Content-Range；308 用 Range 恢复偏移。
3. 响应丢失先用空 PUT + `Content-Range: bytes */总大小` 查询进度。Google 404 只能用原 UUID/元数据加 `restart:true` 请求服务器核验并按需重建会话，不新建 UUID 冒充另一文件。
4. 最终 Google 200/201 后，`POST /v1/uploads/{id}/complete`，使用本次 upload_token、空 JSON；只在 ready + 非空 share_url 时成功。pending 重试完成接口，不重传文件。
5. 保留链接为站点 `share_url`，不要返回 Google 上传会话 URL。站点 `mode=redirect` 的分享 URL 是直接 302 下载链接。实测对分享链接 HEAD 返回 405，因此用不跟随跳转的 GET 验证 302；请求会触发站点自带的下载请求通知，上传完成也可能触发站点自带通知，不额外调用任何消息服务。

401/403、元数据冲突、expired/deleted 等非瞬态错误直接报告；429/5xx/超时最多五次自动重试并尊重 Retry-After。请求要求更长等待时保留回执，稍后恢复，不提前重试。不得为绕过过期状态自动重传或擅自删除旧文件。

只上传用户指定/授权的配置包。已授权的“按之前一样给直链”可沿用该站点和默认有效期；单纯建技能或生成配置不是无限制上传其他文件的授权。

最终返回文件名、站点直链、真实到期时间，并说明到期自动删除。不要回显令牌、受限 token、Google 上传 URL 或原始异常全文。
