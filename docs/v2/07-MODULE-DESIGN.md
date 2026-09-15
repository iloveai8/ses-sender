# ses-sender v2 模块详细设计（MODULE-DESIGN）

> **文档定位**：详细设计②/②。六模块的 Go 接口签名、SQL 查询清单、事务边界——P4~P8 编码的直接依据。
> **版本**：v0.1（待评审③）｜**上游**：03-MODULES.md（分层）/ 04-DATA-MODEL.md（表）/ 05-API-SPEC.md（契约）
> 约定：接口定义在**消费方**（Go 惯例）；`herror` = `{status int, detail string}` 统一错误；所有时间 `time.Time` UTC。

---

## 0. 公共件（pkg/httpx）

```go
// 统一错误：业务层唯一错误出口，中间件渲染 {"detail": "..."}
func NewError(status int, detail string) *Error          // detail 支持插值模板
func (e *Error) Error() string

// 分页响应包（blacklist 端点用 WithoutPageSize 变体——毛边#2）
type Page[T any] struct{ Items []T; Total, PageNum, PageSize, TotalPages int }

// JSONTime：序列化 "2006-01-02T15:04:05"（无 Z，契约 7 条）
type JSONTime time.Time

// 422 复刻：pydantic v2 形状（missing / string_type / greater_than_equal 三类）
func Unprocessable(field, typ, msg string) *Error

// 中间件链（顺序=责任链）：Recover → RequestLog → CORS → CORS 预检短路
```

## 1. account

```go
type TokenSigner interface {
    CreateAccessToken(sub string) string          // HS256, claims 恰 {sub, exp: now+24h 整秒}
    ParseSub(token string) (string, error)        // 验签+exp，失败统一 ErrInvalidAuth
}
type Authenticator interface {
    // 中间件消费：JWT→加载用户；返回 (user, *httpx.Error)
    Authenticate(ctx, authHeader string) (*User, *httpx.Error)   // 401 三段文案矩阵
    FromQueryToken(token string) (*User, *httpx.Error)           // Excel 下载用
}
type QuotaPolicy interface {
    TodayUsage(ctx, userID int) (limit, sent, remaining int)     // sent=SUM(total_contacts) UTC日界
}
type RateLimiter interface {   // 进程内存，语义=core/ratelimit.py
    ClientIP(r *http.Request) string
    IsLocked(ip string) int; RecordFailure(ip string) int; RecordSuccess(ip string)
}
type PasswordHasher interface { Hash(pw string) string; Verify(hash, pw string) bool }  // bcrypt12→$2b$
```

**SQL 清单（db/queries.sql）**：`GetUserByUsername` / `GetUserByEmail` / `GetUserByID` / `ListUsers` / `CreateUser` / `UpdateUser`(动态字段) / `UpdateContactEmail` / `GetUserUnsubConfig` / `SetUserUnsubConfig` / `SumTodayContactsByUser`
**事务边界**：用户创建/更新各自单事务；登录无事务（只读）。
**初始化**：`EnsureDefaultAdmin`（admin/admin123，不存在才建）——main 启动序列调用。

## 2. contact

```go
type GroupRepo interface {
    List(ctx, userID int, search string, pg PageReq) ([]Group, int)
    Create / Update / Delete(ctx, userID, id) (*herror)      // Delete=软级联：先删 contacts 再删 group（同事务）
    CountContacts(ctx, groupIDs []int) map[int]int
}
type ContactSource interface {   // campaign 建单消费
    Enumerate(ctx, groupID int) []ContactView                 // {Email, Name, AttributesJSON}
}
type ContactRepo interface { List(search,pg) / Create / Delete(校验组归属) }
```

**SQL**：`ListGroupsWithCount`(LEFT JOIN 聚合或子查询，对齐现网内存计数口径) / `CRUD×2` / `DeleteContactsByGroup`
**Excel**：`platform/xlsxio`——`ParseImport(r io.Reader) ([]ContactView, error)`（首两列姓名邮箱、3 列起属性、空邮箱跳过、数值/日期单元格字符串化=openpyxl str 等价）；`WriteExport(w, rows)` / `WriteTemplate(w)`。
**下载鉴权**：`?token=` → Authenticator.FromQueryToken。

## 3. template

```go
type TemplateRepo interface {
    List(ctx, userID) []Template
    Get(ctx, userID, id) (*Template, bool)
    Create / Update / Delete
    ListAttachments / AddAttachment / DeleteAttachment     // ≤5 个、≤10MB 校验在 service
}
type Renderer interface {                                    // delivery 消费
    Render(tpl *Template, vars Vars) (subject, html, text string)
    LoadAttachments(ctx, tplID int) []Attachment            // 读磁盘原始字节
}
type AIOptimizer interface {                                 // 可选，未配置 provider → 400
    Optimize(ctx, req OptimizeReq) (*OptimizeResult, *httpx.Error)
    Evaluate(ctx, req EvaluateReq) (*EvaluateResult, *httpx.Error)   // 8 维×N 模型并发
    DimensionFix(ctx, req FixReq) (*FixResult, *httpx.Error)
}
```

**渲染顺序（契约）**：`{{name}}` → `{{email}}` → `{{unsubscribe_url}}`(空→`#`) → 自定义属性按 map 迭代序。
**SES 双写**：`platform/awsx.SESTemplate`：`Create/Update/Delete(sesName, subject, html)`；ses_name=`u{uid}_{8hex}`；SES 失败→500 文案，删除失败静默。
**_parse_ai_json 移植**：```json 围栏剥离 → 直接 Unmarshal → 失败走手写扫描器（括号深度匹配 suggestions + 转义感知的字符串截取 + 三种反转义）——逐行移植 Python 实现。

## 4. campaign

```go
type BatchCreator interface {         // HTTP handler 与 Scheduler 共用
    Create(ctx, cmd BatchCmd) (*BatchResult, *httpx.Error)
    // cmd: {UserID, SourceEmail, TemplateID, GroupID}
    // 内部：QuotaPolicy 校验 → TemplateRepo 归属 → GroupRepo 归属 → ContactSource 枚举
    //       → UnsubRepo 过滤 → 去重 → 黑名单标 Unsubscribed/Failed 延后到引擎
    //       → 单事务：INSERT batch(queued) + INSERT details(Pending/Unsubscribed) + 回填 job_id
}
type MetricsReader interface {
    Batch(ctx, batchID) (*BatchMetrics, bool)          // 7 计数+3 比率+receipt_available（SQL COUNT）
    Dashboard(ctx, userID) (*Dashboard)                // summary/delivery/trend7/recent5（需求 D8）
    AdminStats(ctx) (*AdminStats)                       // D9①②③
}
type ScheduleRepo interface { List/Create/Update/Delete; Due(now) []ScheduledJob }
type UnsubRepo interface {
    List(filter, pg); Delete(权限校验 403); BatchDelete;
    IsUnsubscribedSet(ctx, sourceEmail string) map[string]struct{}   // 建单时批量取
}
type DetailReader interface { List(filters, pg) / ExportRowStream(filters) ≤10000 }
```

**SQL（关键口径锁定）**：
- 配额：`SELECT COALESCE(SUM(total_contacts),0) FROM sending_jobs WHERE user_id=? AND created_at>=?`
- 指标：`SELECT send_status, COUNT(*) ... GROUP BY` ×（delivery_status / open_count>0 / click_count>0 / complaint_time IS NOT NULL）或等价 7 条 COUNT——与 Python `_count` 版本对拍
- success_rate（admin）：**job 数级** `SUM(status='success')/COUNT(*)`
**事务边界**：BatchCreator 单事务（batch+details+回填）；指标全只读。

## 5. delivery（接口在 06-ENGINE-DESIGN.md，此处补交互面）

```go
// 对 server/ 暴露
func MountUnsubscribe(r gin.RouterGroup, h UnsubHandler)   // GET(HTML页)/POST(纯文本 ok)
// 对 campaign 依赖（接口消费方=delivery）
type BatchStore interface {          // 由 campaign 提供实现
    ClaimQueued(ctx, limit int) []QueuedBatch            // 置 sending 并返回
    LoadPending(ctx, batch) []PendingDetail
    HealStuck(ctx)                                          // 06§3 步骤①
}
type UnsubToken interface { Generate(email, source string) string; Verify(token) (email, source string, ok bool) }
```

**UnsubHandler**：GET 三态页（选择页/Already/Invalid·Expired，配置来自 settings 的 `get_unsub_page_config` 等价合并链：用户级→系统级→默认）；POST：token 取 query→form(`token`/`List-Unsubscribe`)，reason 默认 "one-click" 截 64，幂等 INSERT IGNORE 语义（存在查询判重）。

## 6. system

```go
type SettingsRepo interface {
    GetAll(ctx) map[string]string         // 40 键白名单，缺失补 ""，默认值注入，秘密键→*_has_* 布尔
    Put(ctx, kv map[string]string)        // 白名单过滤；秘密键空串=跳过；"__CLEAR__"=清空
    Get(ctx, key string) string
    GetAIModels / PutAIModels / TestProvider(ctx, cfg) (*TestResult, error)
}
type Blacklist interface {                // 进程级单实例（组合根构造），RWMutex 保护 map[string]struct{}
    Start(ctx)                            // 同步全量加载 + 60s 刷新 goroutine
    IsBlacklisted(email string) bool      // lower+trim 后查
    Add(email) / Remove(email) / Reload() / Count() int
}
type IdentityAdmin interface {            // 经 MailProvider（L3）
    ListIdentities(ctx) / VerifyEmail / VerifyDomain / Reputation
}
type OpsConsole interface { TailLogs(lines) / DownloadLogs / RunSQL(sql, allowWrite) }
```

**RunSQL 类型映射（契约）**：DECIMAL→float64、DATETIME→JSONTime、BLOB→utf-8 字符串、NULL→nil；白名单 `SELECT/SHOW/DESCRIBE/EXPLAIN` 前缀判定。

## 7. platform

```go
// config：全量环境变量（=现网 core/config.py 键集），DATABASE_URL 兼容 mysql+pymysql:// scheme 翻译
// database：*sql.DB（parseTime&loc=UTC）；goose 引导三态（04§4）；Tx(ctx, fn) 事务助手
// awsx：SESProvider（MailProvider+EventSource 唯一实现）、SESTemplate、S3Upload、CloudWatch Reputation
// log：slog → 双写 stdout + logs/app.log（10MB×5 轮转），格式 "2006-01-02 15:04:05,000 [name] LEVEL: msg"
// storage：uploads/{images,attachments} 路径规则与 Python 逐字节一致
```

## 8. 组合根装配序（main.go，all 模式）

```
config → log → db(+goose 引导) → 健康路由 GET /（无依赖，最先可用）
→ PasswordHasher/TokenSigner → EnsureDefaultAdmin → Blacklist.Start
→ 按模块构造 repo/service → server 装配路由（顺序=Python include 序）
→ --mode∈{all,worker}：Engine(scanner+workers) / EventSource / Scheduler 启动
→ SIGTERM → 优雅停机（06§9）→ api 模式则全程跳过后三行
```

## 9. 测试布局

```
单元：service 层（mock 接口）——对应 Python 58 条测试锚定的字面量全数保留
集成：httptest + docker MySQL——每域端点含错误分支（corpus 负例子集）
对拍：tools/harness（P0 契约件）——P4 起每域合入前跑全绿
```
