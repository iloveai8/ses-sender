# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

AWS SES bulk email management platform. Go backend (gin + sqlc + goose) + Next.js frontend + MySQL, deployed via Docker Compose / AWS ECS Fargate.

> **v2 分支说明（2026-09-15）**：后端已由 Python 全量重写为 Go（目录仍为 `backend/`）。Python 版仅存于 git 历史（`git show v0.1.5:backend/...`），生产仍运行 v0.1.5（Python），切换见 `docs/v2/` 计划。Go 实现的设计文档在 `docs/v2/`（需求/架构/模块/数据/API/引擎六份）。

## Build & Run Commands

```bash
# ── Go 后端（backend/）────────────────────────────
cd backend
make build          # 编译到 bin/ses-sender
make test           # go test ./... -race
make lint           # golangci-lint
make run            # 本地运行（读 config.yaml / 环境变量）
cp config/config.example.yaml config/config.yaml   # 首次：复制配置模板（gitignored）

# ── Docker 全栈（仓库根）─────────────────────────────
docker-compose up -d --build          # mysql + backend(Go) + frontend + mcp
docker-compose up -d mysql            # 仅本地 MySQL（:3306）
docker-compose logs -f backend

# ── 发布（tag 驱动，详见 docs/DEPLOY.md）────────────
cd deploy && make deploy ENV=prod TAG=vX.Y.Z [PART=frontend|backend|all]

# ── 数据库迁移（goose，基线=alembic head 对齐）────────
# 启动时自动执行三态引导（全新库建表/存量库标记基线/增量升级），见 docs/v2/04-DATA-MODEL.md §4
```

## Architecture

```
Frontend (Next.js :3000) ──/api proxy──▶ Backend (Go :8000) ──▶ MySQL 8
                                               │
                                      ┌────────┼────────┐
                                      ▼        ▼        ▼
                                    AWS SES  CloudWatch  SQS
                                      │                  ▲
                                      └──▶ SNS ─────────┘
```

- Frontend reverse-proxies all `/api/*` to backend. Only port 3000 exposed.
- 单二进制多角色：`ses-sender serve --mode=all|api|worker`（cobra）。生产首发 all 模式（API+引擎+调度一体）；api/worker 拆分是切换后的部署演进。
- 后台子系统以 goroutine 运行（all/worker 模式）：SQS 事件轮询、Scheduler（30s）、Sender 引擎（Scanner 5s + Worker 池）。
- compose 第 4 个服务 `mcp`（Node :8808）把平台操作暴露为 MCP 工具（3 入口：index.js 全量 stdio / index-lite.js 精简 / index-remote.js HTTP）。

### Go 后端结构（backend/）

```
cmd/ses-sender/        cobra：main(薄入口)/root/serve(组合根)/healthcheck(容器探针,零初始化纪律)
internal/
├── app/               HTTP 装配：路由（按旧版 include 顺序）、中间件链、优雅停机
├── httpx/             统一错误体 {"detail":...}、分页包、JSONTime、中间件
├── platform/          config(viper三级合并) / database(sqlc+goose) / awsx / storage / xlsxio / log
├── account/           登录/JWT/限速/用户/配额/个人设置（+sso 占位）
├── contact/           客群/联系人/Excel
├── template/          模板/附件/AI(可选)
├── campaign/          建单四过滤/批次指标/定时任务/退订管理
├── delivery/          发送引擎/事件管道/SQS/Scheduler/退订端点（不 import gin）
└── system/            渠道配置/发件身份/黑名单/设置/运维
migrations/            goose：00001_baseline（=旧 alembic head 全量 DDL，11 表）
contracts/             openapi.json 冻结契约 + 对拍语料/golden/fixtures
tools/harness/         对拍工具 record/diff/dbdiff
```

分层铁律：只能向下依赖（app→域→platform）；域间只认接口；delivery 不碰 HTTP。语义规格见 `docs/v2/06-ENGINE-DESIGN.md`（引擎）与 `07-MODULE-DESIGN.md`（各域接口）。

## Sender Engine（delivery 域）

`DB(queued 批次) → Scanner(5s 一轮,每轮≤5 批) → buffered channel(容量=并发×速率×4) → Worker 池(默认2)`。速率：每 Worker 每秒上限，0=自动 `floor(SES配额快照/并发)`；可选固定窗口。断点恢复/防重：明细状态机（Pending→Success/Failed/Unsubscribed）+ 发送前 Pending 复查。完整规格（状态机/崩溃场景表/事件管道）：`docs/v2/06-ENGINE-DESIGN.md`。

## Frontend

Next.js 16 + React 19 + Tailwind 4 + TS，单页三模式（login/admin/user），共享组件在 `app/components/shared.tsx`，页面在 `app/pages/{user,admin,shared}/`。i18n `app/i18n/`（zh/en）。API 基址 `/api`（NEXT_PUBLIC_API_URL 是休眠后门，永远别设）。

## Local Dev & Git

- 配置：`backend/config/` 只入库一个模板 `config.example.yaml`；本地要跑时 `cp config.example.yaml config.yaml`（gitignored）。多环境：`APP_ENV=test` 自动加载 `config.test.yaml`（同样 gitignored）。配置三级合并：flag > 环境变量 > yaml > 默认（变量名沿用旧版，生产 SSM 注入零改动）。compose 的 backend 服务**直接挂载 `./backend/config` 进容器**（本地 compose 与裸跑共用同一份配置；DATABASE_URL 例外——容器内 DB 主机名是 `mysql`，由 compose env 覆盖）。本地调真 SES：`export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=...` 后再 up。
- 本地栈：`docker-compose up -d mysql` → `cd backend && make run` → `cd frontend && npm run dev`（`.env.local` 的 BACKEND_URL 指向 :8000）。
- ECS 生产：deploy/ 目录 tag 驱动发布，入口 ALB。运维手册 `docs/INFRA.md` + `docs/DEPLOY.md`。
- Git：origin=内部 GitLab；活跃开发分支 `v2`（Go 重写）；`dev`/`main` 仍是 Python 基线。

## Code Style

- Go：gofmt、Tab 缩进；`golangci-lint`（.golangci.yml）+ `go vet` 进 CI 强制；注释用中文。
- 业务逻辑在 service 层（handler 零业务）；SQL 全部 sqlc 手写（emit_json_tags=false，DTO 手写保契约字段名）。
- 测试：testify + httptest；错误字面量是契约（{"detail":...} 中文文案逐字对齐旧版）。
- 迁移：goose 增量从 00002 起；禁止修改已有迁移文件。

## Important Patterns（业务规则不变，语义见 docs/v2/）

- 异步发送：API 只建单（queued），引擎异步消费。
- 建单四过滤顺序：退订过滤 → 同邮箱去重 → 黑名单拦截 → 配额校验（SUM(total_contacts) UTC 日界，含退订跳过者）。
- 退订：HMAC token（hex 截 16 + 带 padding URL-safe base64），按（收件邮箱,发件邮箱）对过滤。
- 模板变量：`{{name}}` `{{email}}` `{{unsubscribe_url}}`(空→"#") + 自定义属性。
- AI 优化为可选模块（Bedrock/OpenAI 兼容，未配置即 400）。
- 定时：once/daily/weekly/monthly，`_calc_next_run` 全 UTC，边界用例表见 06 §8。

## Key Files

- `backend/cmd/ses-sender/` — 入口与子命令
- `backend/internal/app/server.go` — HTTP 装配
- `backend/internal/platform/config/config.go` — 配置三级合并
- `backend/migrations/00001_baseline.sql` — goose 基线（11 表）
- `backend/contracts/openapi.json` — 冻结契约（86 端点，验收权威）
- `docs/v2/` — 全部设计文档（P0 需求 → P2 详细设计）

## Do Not

- Do not hardcode AWS region — always use config（AWS_REGION）
- Do not use SES v1 bulk send — 逐收件人 SES v2 `send_email`
- Do not expose backend port 8000 externally（compose 用 expose；只有前端 3000 对外）
- Do not add `NEXT_PUBLIC_*` env vars for API URL
- Do not modify existing migration files — goose 增量从 00002 起
- Do not use `docker-compose restart` to apply env changes — `down && up -d`
- healthcheck 子命令保持零初始化（ADR-6 纪律）：不读配置、不连 DB、不 import 业务包
