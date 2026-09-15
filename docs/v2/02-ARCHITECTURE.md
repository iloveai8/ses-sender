# ses-sender v2 架构设计（ARCHITECTURE）

> **文档定位**：概要设计①/④。回答"系统怎么搭"——总体结构、进程形态、技术选型、渠道接口边界、设计模式应用。
> **版本**：v0.1（待评审②）｜**上游**：01-REQUIREMENTS.md｜**姊妹**：03-MODULES.md（模块分层）/ 04-DATA-MODEL.md（数据结构）/ 05-API-SPEC.md（API 设计）

---

## 1. 总体架构

```
                          ┌──────────────────────────────┐
        浏览器 ──────────►│  Next.js 前端（不改动一行）    │
                          │  /api/* 反代 → 后端:8000       │
                          └──────────────┬───────────────┘
                                         ▼
┌────────────────────────────────────────────────────────────────┐
│  ses-sender（Go 单二进制，-mode = all / api / worker）           │
│                                                                │
│  server/          路由装配 · 中间件链 · 优雅停机 · 静态/文档      │
│  ──────────────────────────────────────────────────────────────│
│  modules/（六模块，业务层，模块间只认接口）                        │
│   account   contact   template   campaign   delivery   system   │
│  ──────────────────────────────────────────────────────────────│
│  platform/（基建层，零业务语义）                                   │
│   config · database(sqlc+goose) · awsx · storage · xlsxio · log │
│  ──────────────────────────────────────────────────────────────│
│  渠道接口边界（第一步唯一预留）                                    │
│   MailProvider ──► SesProvider（唯一实现）                       │
│   EventSource  ──► SesQueueSource（SQS 拉模式）                  │
└───────────────┬──────────────────────────────┬─────────────────┘
                ▼                              ▼
          AWS SES/SQS/S3/Bedrock          MySQL 8（结构不动）
```

**部署拓扑不变**：ECS Fargate 单实例 512CPU/1GB、SSM 注入密钥、Service Connect、CloudWatch 日志——Go 版只是替换容器镜像与 taskdef（健康检查命令改为 `["CMD","/app/ses-sender","healthcheck"]`）。

## 2. 进程形态：单二进制三模式

```
-mode=all（首发，生产用）   HTTP API + 引擎 + Scheduler + SQS + 黑名单缓存
-mode=api                  仅 HTTP API（+黑名单只读刷新）——灰度期用，引擎仍归 Python
-mode=worker               引擎 + Scheduler + SQS（+独立健康端口 8001）——切换后拆分演进用
```

- main.go 是**组合根**（cobra `serve` 子命令入口）：`--mode` 旗子 → 构造依赖 → 装配对应组件集，唯一的"知道全部实现"的地方
- 价值：api/worker 拆分从"改代码"变成"改部署"；切换前生产恒 all 模式，行为与现状一致

## 3. 两个后台子系统（与 HTTP 平级，不 import gin）

### 3.1 发送引擎（delivery 模块内）

```
Scanner goroutine ──► buffered channel（容量=并发×速率×4）──► Worker 池（默认 2）
   │  每5s一轮：自愈修正→取≤5个queued批次→逐明细入队             │  每Worker每秒限速
   │  队满则本轮中止（批次留下轮）                                │  发送前复查明细=Pending
   ▼                                                          ▼
 状态机自愈（Pending+有回执→Success；真Pending=0→终态判定）    MailProvider.Send()
                                                              成功/失败 → 明细行状态
```

### 3.2 事件管道

```
EventSource(SES/SQS拉) ──► 归一化(SNS拆包→统一事件) ──► 幂等落库 ──► 明细行回执字段
                                                                       (Pending先改Success)
Scheduler goroutine：每30s查到期任务 → 复用 campaign 建单逻辑
```

### 3.3 优雅停机（比 Python 版多的能力）

SIGTERM → 停止接收新任务 → 排空 channel（在途批次收尾）→ 退出。ECS 滚动更新的 30s 宽限期内完成。

## 4. 渠道接口边界（第一步唯一预留，详细规格见 05-PROVIDERS 章节并入本文档附录 A）

```go
// 发信侧：业务代码只认它，永不直调 SES SDK
type MailProvider interface {
    Send(ctx context.Context, msg OutboundEmail) (messageID string, err error)
    GetQuota(ctx context.Context) (Quota, error)          // 配额/速率
    // + 身份验证（域名/邮箱验证、状态、DKIM）——system 模块消费
}

// 回执侧：拉(SQS)/推(webhook)两种传输形状的归一
type EventSource interface {
    Start(ctx context.Context, sink EventSink) error
}
```

- 现在只有一个实现：`SesProvider` / `SesQueueSource`
- 接口本身按"三家同构"设计（勘察结论：发/验/回执/配额四维一致），第二步加腾讯/阿里时核心零改动

## 5. 技术选型（定案及理由）

| 层 | 选择 | 理由 | 落点 |
|----|------|------|------|
| Web | **gin** | 国内事实标准=维护性（接手零学习成本）；只用其路由+中间件，错误体/422 由自有中间件统一渲染，**不启用 gin 自带错误格式**（契约零污染） | server/ + 各 handler |
| DB 访问 | **sqlc** | 手写 SQL 与 SQLAlchemy 产出可逐条对拍；生成物类型安全且无运行时反射 | platform/database + 各模块 db/ |
| 迁移 | **goose**（嵌入二进制） | 启动自动 upgrade，复刻现 Alembic-on-startup 行为；基线对齐 head `e3f4a5b6c7d7` | migrations/ |
| AWS | aws-sdk-go-v2 | 官方唯一维护版本 | platform/awsx |
| 认证 | golang-jwt v5 + x/crypto/bcrypt | 标准件；字节兼容三件套的实现基础 | modules/account |
| Excel | excelize | 生态唯一成熟选项 | platform/xlsxio |
| 日志 | **slog**（标准库） | 结构化；格式复刻现网 `2006-01-02 15:04:05,000 [name] LEVEL: msg` 保 /admin/logs 兼容 | platform/log |
| 测试 | testing + testify + httptest | CI 全自动 | 各模块 _test.go |
| CLI | **cobra**（用户拍板） | 单二进制多子命令：`serve`（组合根）/ `healthcheck`（容器探针）；为将来 `migrate`·`admin` 等运维子命令铺路 | cmd/ses-sender |
| 配置 | **viper**（cobra 姊妹件，用户建议） | 三级合并：flag > 环境变量 > config.yaml > 默认值。yaml 给人读（模板 config.example.yaml）、env 沿用 Python 版变量名（生产 taskdef/SSM 注入零改动）、密钥只走 env 不进 yaml | platform/config |
| 镜像 | 多阶段构建 → debian-slim，单二进制 `/app/ses-sender` | WORKDIR 与 Python 版一致（存量附件绝对路径兼容） | Dockerfile |

## 6. 设计模式应用地图（8 个位置，克制清单见 03-MODULES）

| 位置 | 模式 | 作用 |
|------|------|------|
| 渠道层 | 策略 + 注册表工厂 | MailProvider 可替换、可注册扩展 |
| 事件传输 | 适配器 | SQS拉/webhook推 归一到 EventSink |
| 横切（重试/指标） | 装饰器 | 包在 MailProvider 外，不污染实现 |
| HTTP 中间件 | 责任链 | recover→requestlog→CORS→JWT→admin |
| 错误处理 | 统一错误类型 | `herror{status, detail}`，中间件唯一出口渲染 `{"detail":...}` |
| 数据访问 | 仓储 | service 定义小接口，sqlc 生成物是实现 |
| 引擎 | 生产者-消费者 + 令牌桶 | Scanner→channel→Worker池；x/time/rate 限速 |
| 装配 | 组合根 + 构造注入 | main.go 显式构造，杀全局单例 |

**不用**：模板方法（继承式）、观察者/事件总线、桥接、Singleton、反射工厂——全系统 80% 代码是普通函数。

## 7. 目录结构（对齐 Go 社区标准布局）

```
backend/
├── cmd/
│   └── ses-sender/               # cobra 标准拆分，一文件一命令（cobra-cli 脚手架同构）
│       ├── main.go               # 薄入口：仅 rootCmd.Execute()，无任何逻辑
│       ├── root.go               # 根命令 + 全局 flag（--config 等）
│       ├── serve.go              # 子命令 serve --mode=all|api|worker（组合根）
│       └── healthcheck.go        # 子命令 healthcheck（容器探针；纪律：零初始化，纯 http.Get）
│                                  # 将来扩展：migrate.go / admin.go / version.go
├── internal/                     # 编译器强制私有（Go 工具链保证，外部无法 import）
│   ├── app/                      # HTTP 装配：路由注册（按 Python include 顺序）、中间件链、优雅停机
│   ├── httpx/                    # 共享横切件：herror 错误体、分页包、JSONTime、中间件实现
│   ├── platform/                 # 基建（零业务语义）：config/ database/ awsx/ storage/ xlsxio/ log/
│   ├── account/                  # ┐
│   ├── contact/                  # │
│   ├── template/                 # │ 六域平铺——Go 惯例：包名即分类，
│   ├── campaign/                 # │ 不套 Java 式目录分类学
│   ├── delivery/                 # │ （delivery 含 engine/：scanner.go worker.go window.go）
│   └── system/                   # ┘
├── migrations/                   # goose：00001_baseline.sql + 后续增量
├── contracts/                    # openapi.json 冻结件 + corpus + golden + fixtures
├── tools/harness/                # 对拍 harness：record / diff / dbdiff
├── .golangci.yml                 # lint 规则（CI 第一道门）
├── go.mod / go.sum / Makefile / Dockerfile / .gitlab-ci.yml
```

**遵循的 Go 规范要点**：① 包名全小写单词、无下划线无驼峰；② `internal/` 私有性由编译器强制；③ 不设根级 `pkg/`（无私有代码需对外发布）；④ `main.go` 保持薄入口，逻辑全在子命令文件；⑤ `gofmt`/`go vet`/`golangci-lint` 进 CI 强制。

## 8. 过渡期架构（与 Python 并存，P10 之前）

```
生产集群（过渡期）：
  backend      现有 ECS 服务（镜像换 Go，-mode=api 侧车浸泡→加权灰度）
  frontend-go  临时灰度服务（同前端镜像，BACKEND_URL 指向 Go 实例）→ /api/* 灰度入口
  （Python v0.1.5 镜像保留在 ECR 与 git tag，回退=重发旧 tag，秒级）

  同库协同：灰度期引擎/Scheduler 仍由单一实例持有（Python 或 Go 其一），无双跑
  最终切换：Go 发 ENABLE_SENDER=true 版，0/100 策略保证引擎零双跑交接
```

## 9. 架构决策记录（ADR 摘要）

| # | 决策 | 备选 | 取舍理由 |
|---|------|------|---------|
| ADR-1 | gin 而非 chi/标准库 | chi 更纯净 | 维护性=团队熟悉度优先；框架隔离在薄层，将来可换 |
| ADR-2 | sqlc 而非 GORM | GORM 开发快 | 契约对拍需要 SQL 显式可控；GORM 隐式行为是排错黑洞 |
| ADR-3 | 单二进制三模式 而非 两个二进制 | 拆开更清晰 | 同一代码路径 all 模式=现网行为，拆分是部署动作不是代码分叉 |
| ADR-4 | 契约照抄（含毛边） 而非 顺手修复 | 修复更优雅 | 重写一次只换一个变量（语言）；修毛边=引入第二个变量，归属第二步 |
| ADR-5 | goose 嵌入 而非 独立迁移服务 | 独立更可控 | 复刻"启动自动迁移"现状；运维面不变 |
| ADR-6 | cobra 单二进制 而非 双 main | 双 main 构造隔离更强 | **用户决策**：将来 migrate/admin 等运维子命令复用同一入口更顺；接受"包初始化传染"的理论风险（探针子命令须保持零初始化纪律） |

## 附录 A：三家渠道映射矩阵（备忘，第二步启用）

| 能力 | AWS SES（现在做） | 腾讯云 SES | 阿里云 DirectMail |
|------|-----------------|-----------|------------------|
| 发送 | v2 send_email | SendEmail | SendMail |
| 回执 | SNS→SQS 拉 | webhook 推 | MNS/回调 |
| 身份 | 域名/邮箱+DKIM | 发信地址+域名 | 发信地址+域名 |
| 信誉 | CloudWatch | 弱 | 弱（能力探测隐藏） |
```
