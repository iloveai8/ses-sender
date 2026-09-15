# ses-sender v2 模块分层设计（MODULES）

> **文档定位**：概要设计②/④。回答"代码怎么切"——模块边界、依赖规则、每模块对外接口、一层请求的完整流转。
> **版本**：v0.1（待评审②）｜**上游**：02-ARCHITECTURE.md

---

## 1. 分层总图（自上而下，只能向下依赖）

```
┌────────────────────────────────────────────────────┐
│ L1  app/          HTTP 装配层（gin 路由/中间件/停机） │  知道所有模块的 router
├────────────────────────────────────────────────────┤
│ L2  六域平铺于 internal/                             │  模块之间只通过接口交往
│     account contact template campaign delivery system│
├────────────────────────────────────────────────────┤
│ L3  渠道接口        MailProvider / EventSource       │  唯一的外部服务抽象
├────────────────────────────────────────────────────┤
│ L4  platform/      基建层（零业务语义）               │  config/db/awsx/storage
└────────────────────────────────────────────────────┘
```

**三条铁律**（违反任何一条=设计错误）：
1. **只能向下**：L1→L2→L3/L4；L4 不知道任何业务概念
2. **模块平级**：L2 内模块互不 import 对方内部实现，只 import 对方暴露的接口（Go 惯例：接口定义在消费方）
3. **delivery 不碰 HTTP**：引擎/事件/调度是纯后台子系统，不 import gin——这是 -mode 拆分免改业务代码的保证

## 2. 六模块职责与对外接口

### 2.1 account —— 谁在用系统

| 项 | 内容 |
|---|------|
| 职责 | 登录/JWT 签发验签/登录限速/用户 CRUD/配额策略/个人设置（收件邮箱、退订页配置）/SSO 404 占位 |
| 承载需求 | A1-A5、F6 占位 |
| 对外接口 | `Authenticator`（JWT→User）、`QuotaPolicy`（配额查询与校验）、`UserRepo`、`TokenSigner` |
| 依赖 | platform、pkg/httpx |

### 2.2 contact —— 发给谁

| 项 | 内容 |
|---|------|
| 职责 | 客群/联系人 CRUD、搜索分页、Excel 导入导出/模板、query-token 下载鉴权 |
| 承载需求 | B1-B4 |
| 对外接口 | `GroupRepo`、`ContactSource`（按客群枚举 {email,name,attributes}——campaign 建单消费） |
| 依赖 | platform（xlsxio） |

### 2.3 template —— 发什么

| 项 | 内容 |
|---|------|
| 职责 | 模板 CRUD（SES 双写）、附件管理、变量渲染、AI 三端点（可选，配 provider 才可用） |
| 承载需求 | C1-C4 |
| 对外接口 | `TemplateRepo`、`Renderer`（变量替换——delivery 发送前消费）、`AttachmentLoader` |
| 依赖 | platform（awsx 的 SES 模板操作） |

### 2.4 campaign —— 发送的业务编排

| 项 | 内容 |
|---|------|
| 职责 | 建单四过滤（退订→去重→黑名单→配额）、批次与明细写库、批次列表/进度/指标/明细/导出、定时任务 CRUD 与触发执行、退订管理、测试邮件 |
| 承载需求 | D1-D7 |
| 对外接口 | `BatchCreator`（delivery/Scheduler 复用）、`MetricsReader`、`ScheduleRepo`、`UnsubRepo` |
| 依赖 | account(配额)、contact(收件人)、template(模板)——通过接口 |

### 2.5 delivery —— 真正发出去+收回来（唯一接触渠道层的模块）

| 项 | 内容 |
|---|------|
| 职责 | 发送引擎（Scanner/Worker池/限速窗口）、明细状态机推进、事件管道（归一/幂等/落库）、SQS 轮询+毒消息策略、Scheduler 线程、退订端点（GET页/POST one-click）与 token 算法 |
| 承载需求 | E1-E4 |
| 对外接口 | `Engine`（Start/Stop）、`EventSink`（事件管道入口）、`UnsubToken`（生成/验证）、`UnsubPageRenderer` |
| 依赖 | MailProvider/EventSource（L3）、campaign 的批次读取接口 |

### 2.6 system —— 系统自身管理

| 项 | 内容 |
|---|------|
| 职责 | 系统设置 KV（秘密脱敏/__CLEAR__ 语义）、AI provider 配置与连通测试、黑名单 CRUD/导入/**内存缓存**、SES 身份列表/验证/信誉（经 MailProvider 身份接口）、运维（日志/SQL控制台） |
| 承载需求 | F1-F5 |
| 对外接口 | `Blacklist`（内存 Set：IsBlacklisted/add/remove/reload——campaign 建单与 delivery 发送瞬间消费）、`SettingsRepo` |
| 依赖 | platform、L3 身份接口 |

## 3. 一层请求的完整流转（以 POST /send-bulk 为例）

```
gin 路由命中
 → 中间件链：recover → requestlog → CORS → JWT(account.Authenticator) → 注入 user
 → campaign.handler：绑定请求体（PascalCase 契约绑定）
 → campaign.service.BatchCreator：
     account.QuotaPolicy 校验配额(429?)
     template.TemplateRepo 取模板(404?)
     contact.ContactSource 枚举收件人(404?)
     过滤：UnsubRepo 退订对过滤 → 同邮箱去重
     一个事务：写 sending_batches(queued) + 明细行(active=Pending / skipped=Unsubscribed)
 → 返回 {batch_id, total_contacts, active_contacts, skipped_unsubscribed, ...}

（异步，另一条线）
 Scanner 下一轮拾取 queued 批次 → 逐明细入队
 Worker：黑名单拦截(system.Blacklist) → 明细仍 Pending 复查 → template.Renderer 渲染
        → MailProvider.Send() → 明细行 Success/Failed → 批次统计推进
```

**要点**：API 只建单（同步快），发送全异步（引擎线）——与现网解耦设计一致；两条线只通过数据库批次表衔接。

## 4. 模块间依赖矩阵（import 方向合法性表）

```
            account contact template campaign delivery system
account       -       -       -        -        -       -
contact       -       -       -        -        -       -
template      -       -       -        -        -       -
campaign      ✓       ✓       ✓        -        -       -
delivery      -       -       ✓(渲染)  ✓(批次)  -       ✓(黑名单)
system        -       -       -        -        -       -
（✓=可 import 对方接口包；空=禁止；无任何双向/环形依赖）
```

## 5. 每模块统一内部分层（三层小纪律）

```
internal/<name>/                  # 六域平铺，无包装层
├── handler.go     HTTP 适配：绑定/校验/取 ctx → 调 service → 错误交 httpx 渲染。零业务逻辑
├── service.go     业务规则 + 事务边界 + 对外接口定义（接口定义在消费方则放消费方包）
├── db/            sqlc 生成物（queries.sql 手写 SQL，与 Python ORM 查询等价）
└── *_test.go      单测（service 层 mock 接口）+ 集成测试（httptest+真MySQL）
```

例外：delivery 无 handler（后台子系统）；system 的黑名单缓存是进程级组件（组合根构造单实例注入，非全局变量）。

## 6. 克制清单（明确不引入的"高级"手法）

| 不引入 | 理由 |
|--------|------|
| 微服务/进程间通信 | 单体够用十年；-mode 是拆分的预留不是拆分本身 |
| 领域事件总线 | 模块间就四个真实交互点，接口直调最清晰 |
| DDD 聚合根/CQRS | 表结构与契约冻结，领域建模空间已被锁死，强行上=仪式感 |
| 依赖注入框架（wire/dig） | 构造函数手写，组合根一眼可读 |
| interface{} / 泛型抽象层 | 明确类型优先，反射零容忍 |
