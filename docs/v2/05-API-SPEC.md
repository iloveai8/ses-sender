# ses-sender v2 API 设计（API-SPEC）

> **文档定位**：概要设计④/④。回答"前后端怎么对话"——通用约定、86 端点契约清单、错误文案锚定、豁免表。
> **版本**：v0.1（待评审②）｜**机器可读权威**：`contracts/openapi.json`（FastAPI 导出冻结件，Go 实现以它验收）
> **核心原则：契约=现网行为，一字不改**（含毛边，见 §6 豁免表）

---

## 1. 通用约定（全部照抄现网）

| 项 | 约定 |
|----|------|
| 基址 | 前端代理 `/api/*` → 后端（剥前缀）；Go 版路径与 Python 版**逐字符相同**（不加 /v2——第一步契约冻结） |
| 鉴权 | `Authorization: Bearer <JWT>`；两个特殊面：Excel 下载用 `?token=<JWT>` query 参数；`/unsubscribe*`、`/uploads/*`、`/sso/*`、`GET /`、`/admin/blacklist/template` 无鉴权（后两处为现状照抄） |
| 错误体 | 一律 `{"detail":"<中文文案>"}`；文案**逐字对齐**（锚定表见 §4） |
| 校验失败 422 | FastAPI/pydantic v2 形状：`{"detail":[{"type":"missing","loc":["body","x"],"msg":"Field required",...}]}`——Go 用 `pkg/httpx` 复刻三类高频形状（missing/string_type/greater_than_equal） |
| 分页响应 | `{items, total, page, page_size, total_pages}`；**例外**：`/admin/blacklist` 无 `page_size`（毛边照抄） |
| 时间 | 响应 `YYYY-MM-DDTHH:MM:SS`（无 Z，isoformat 等价）；请求 `scheduled_time` 接受 ISO 串（去 Z 解析） |
| 特殊响应体 | `POST /unsubscribe` → 纯文本 `ok`（400 纯文本 `missing token`/`invalid token`）；SSO 回调 → 307 重定向（本次不实现，占位 404）；xlsx/日志下载 → StreamingResponse |
| CORS | `*` + credentials（Starlette 现行为，头集合以录制为准） |

## 2. 端点契约清单（86 个，按域）

> 完整请求/响应字段见 openapi.json；本表为**行为摘要+鉴权+特殊状态码**，验收以此对照。

### 2.1 auth 域（9）+ main 直挂（2）

```
POST /auth/login                    无鉴权   401/403/429 三态文案矩阵（见需求 A1/A2）
GET  /auth/me                       user
GET  /admin/users                   admin
POST /admin/users                   admin    400 用户名已存在；sender_name>255 → 422
PUT  /admin/users/{id}              admin    404 用户不存在
GET  /user/unsub-config             user     任意 JSON 原样
PUT  /user/unsub-config             user     {"message":"退订页面配置已保存"}
GET  /user/unsub-defaults           user     默认配置（5 原因/#667eea/buttonText）
PUT  /user/contact-email            user     400 收件邮箱不能为空
POST /upload/image                  user     multipart；模式 local|s3 分流；400 白名单/5MB
GET  /                              无鉴权   {"message":"SES Sender API is running"}（健康检查）
+ /uploads/* 静态挂载（无鉴权，目录请求 404）
+ /openapi.json /docs /redoc（冻结静态件，页面内容豁免）
```

### 2.2 identity 域（4，prefix /admin/identities）

```
GET  .../reputation    admin    账号信誉+7天退信/投诉率（CloudWatch，异常降级 0）
GET  ...               admin    身份列表（域名/邮箱判定+DKIM 状态逐个查询）
POST .../verify-email  admin    ?email=
POST .../verify-domain admin    ?domain= → {message, token(TXT)}
```

### 2.3 template 域（14）

```
GET/POST        /user/templates            user     建=SES 双写，ses_name=u{uid}_{8hex}
PUT/DELETE      /user/templates/{id}       user     404 模版不存在；删除 SES 失败静默
GET/POST/PUT/DELETE /admin/templates*      admin    行为与用户版等价（按 admin 本人隔离——毛边）
GET   /user/templates/{id}/attachments     user     元数据列表
POST  /user/templates/{id}/attachments     user     multipart；400 超5个/超10MB
DELETE /user/templates/{id}/attachments/{att_id} user
POST  /ai/optimize-template   user   400 未配置；500 AI 优化失败/格式异常；502 上游
POST  /ai/evaluate-template   user   八维×多模型并发
POST  /ai/dimension-fix       user
GET   /ai-models/available    user   [{id,name,provider_name,provider_type}]
```

### 2.4 contact 域（10）

```
GET/POST/PUT/DELETE /groups(/{id})        user     404 客群不存在或无权操作；删=级联
GET  /groups/{id}/contacts                user     搜索 name|email、分页
POST /contacts                            user     404 客群
DELETE /contacts/{id}                     user
GET  /groups/{id}/contacts/download?token= query鉴权  xlsx 导出
GET  /contacts/template/download?token=   query鉴权  xlsx 模板
POST /groups/{id}/contacts/upload         user     multipart xlsx；400 文件解析失败: ...
```

### 2.5 sending 域（23）

```
POST /send-bulk                        user   请求体 PascalCase {TemplateId,GroupId}（毛边锚定）
                                              400/404/429 文案矩阵（需求 D1/D2/A5）
GET  /sending-jobs(/{batch_id}/metrics|/details|/progress)  user   404 批次不存在；receipt_available 标志
GET  /email-details(/export)           user   四筛选条件；非 admin 只看自己批次
GET  /ses-quota  /user/daily-quota     user
GET  /user/dashboard                   user   summary/delivery(含 receipt_available)/trend7d/recent5
POST /admin/test-email                 admin  message_id 返回
GET  /admin/users/quotas               admin  {uid: 今日已发} dict
GET  /admin/sending-stats  /admin/sending-jobs  admin
GET/POST/PUT/DELETE /scheduled-jobs(/{id})  user   400 时间格式无效/计算下次执行时间失败
GET/DELETE /unsubscribe-list(/{id})  POST /unsubscribe-list/batch-delete  user  恢复文案+403 无权操作
GET  /unsubscribe                     无鉴权  HTML 退订页（配置驱动/已退订页/Invalid link 400）
POST /unsubscribe                     无鉴权  纯文本 ok（one-click RFC8058）
```

### 2.6 settings 域（17）

```
GET/PUT /admin/settings               admin   秘密脱敏 *_has_* / __CLEAR__ 语义
GET/PUT /admin/ai-models              admin   providers JSON 整包
POST /admin/ai-models/test  /admin/settings/test-bedrock  admin  连通性测试
POST /admin/sql                       admin   SELECT/SHOW/DESCRIBE/EXPLAIN 白名单+allow_write
GET  /admin/logs(/download)           admin   尾部 N 行
GET/POST/DELETE /admin/blacklist(/{id})  POST .../batch-delete  POST .../upload  GET .../count
                                              admin（/template 无鉴权=毛边照抄）
```

### 2.7 sso 域（6）：`/sso/providers`、`/sso/{github,google,saml}/{login,callback}` —— **不实现**，路由占位返回与现网一致的 404 行为

## 3. 鉴权错误文案矩阵（401/403/429）

```
无 Authorization 头        401 {"detail":"Not authenticated"} + WWW-Authenticate: Bearer
JWT 解析失败/无 sub        401 {"detail":"无效的认证信息"}
用户不存在或 is_active=F   401 {"detail":"用户不存在或已禁用"}
非 admin 访问 admin 端点   403 {"detail":"需要管理员权限"}
登录锁定期                 429 {"detail":"登录失败次数过多，请 N 分钟后再试"}
```

## 4. 错误文案锚定表（节选高频 30 条；全量随 corpus 生成进 contracts/error-messages.md）

```
用户名或密码错误 / 失败次数过多，已临时锁定，请 10 分钟后再试 / 账户已被禁用
用户名已存在 / 用户不存在 / 需要管理员权限 / 收件邮箱不能为空
模版不存在 / 客群不存在或无权操作 / 客群中没有联系人 / 联系人不存在
今日发送配额已用完（限额 {n} 封），请明天再试 / 今日剩余配额 {a} 封（限额 {b}，已用 {c}），该客群有 {d} 个联系人，超出配额
批次不存在 / 记录不存在 / 无权操作 / 任务不存在 / 时间格式无效 / 计算下次执行时间失败，请检查时间设置
未选择记录 / 已在黑名单中 / 未识别到有效邮箱地址 / 文件解析失败: ...
未配置 AI 模型... / AI 优化失败: ... / AI 返回格式异常，请重试 / 获取修复建议失败: ...
模版「{name}」创建成功 / 已更新 / 已删除 / 客群已删除 / 联系人已删除
已恢复，该邮箱将重新接收邮件 / 退订页面配置已保存 / 收件邮箱已更新 / 配置已保存 / 模型列表已保存
成功导入 {n} 个联系人 / 导入完成：{x} 个新增，{y} 个已存在 / 测试邮件发送成功
发送任务已创建，正在后台执行 / 已删除 / 已恢复 {n} 条记录 / 执行成功，影响 {n} 行
```

## 5. 契约对拍机制（机器验收）

```
corpus.yaml：86 端点 × 正例/权限矩阵/负例（422/401/403/404/429），含锁定触发序列
golden/：Python 录制响应（同种子库、同 SECRET_KEY）
harness diff：Go 起在 :8010 → 重放语料 → 归一化对比（时间戳/batch-id/token/日志内容归一）
豁免表：xlsx 二进制字节（结构级比对）、/docs 页面内容、ETag/Content-Length 头
```

## 6. 契约毛边登记表（照抄清单——重写时不修，第二步再议）

| # | 毛边 | 现状 |
|---|------|------|
| 1 | `BulkSendRequest` PascalCase（TemplateId/GroupId） | 全系统唯一大写请求体 |
| 2 | `/admin/blacklist` 分页无 `page_size` 键 | 其余分页端点都有 |
| 3 | `/admin/templates*` 按 admin 本人隔离 | 名义 admin 实际同用户版 |
| 4 | `/admin/blacklist/template` 无鉴权 | 原代码漏挂 Depends |
| 5 | `POST /unsubscribe` 纯文本响应 | 非 JSON |
| 6 | `daily_send_limit` 空值时 UserOut 返回 500（pydantic int 校验） | Go 侧返回 1000（对齐 ORM 默认语义），登记豁免 |
| 7 | 时间无时区后缀 | isoformat 等价输出 |
| 8 | `sending_jobs.total_batches` 恒 0 的遗留列 | 照抄 |
| 9 | SQS at-least-once：重复 OPEN/CLICK 事件重复累加计数（open_count 虚增） | 照抄现状；去重（按 message_id+eventType）属第二步 |
