# ses-sender

AWS SES 批量邮件管理平台——模板管理、受众管理、批量/定时发送、回执统计、退订闭环，自带 Web 管理面板。

## 技术栈

| 层 | 技术 |
|----|------|
| 后端 | **Go**（gin + sqlc + goose + cobra + viper），单二进制多模式（serve/healthcheck） |
| 前端 | Next.js 16 + React 19 + Tailwind CSS 4（反向代理 `/api/*`，只暴露 3000） |
| 数据库 | MySQL 8（11 张表，goose 迁移） |
| 云服务 | AWS SES（发送）/ SNS+SQS（回执事件）/ S3（图片存储，可选） |
| 部署 | Docker Compose / AWS ECS Fargate（tag 驱动发布） |
| MCP | 内置 MCP server（:8808），把平台操作暴露给 AI 助手 |

## 功能

- 邮件模板：HTML 编辑、变量占位（`{{name}}`/`{{email}}`/`{{unsubscribe_url}}`/自定义属性）、附件、AI 优化（可选，支持 Bedrock / OpenAI 兼容端点）
- 受众管理：客群/联系人、自定义 JSON 属性、Excel 批量导入导出
- 批量发送：异步引擎（Scanner + Worker 池 + 速率跟随 SES 配额）、每日配额、退订/黑名单/去重四重过滤
- 定时发送：once / daily / weekly / monthly
- 回执统计：送达/退信/投诉/打开/点击，SES 事件经 SNS→SQS 回写数据库
- 退订闭环：邮件内退订链接 + RFC 8058 一键退订，HMAC 签名 token，可恢复
- 多用户：账户配额、发件身份（域名/邮箱验证）、管理员面板

## 快速开始

```bash
# 1. 配置
cp backend/config/config.example.yaml backend/config/config.yaml   # 按需修改（数据库地址等）

# 2. 一键起全栈（MySQL + 后端 + 前端 + MCP）
docker-compose up -d

# 3. 访问
# 前端  http://localhost:3000   （默认管理员 admin/admin123，登录后请改密）
```

本地开发：

```bash
docker-compose up -d mysql            # 只起数据库
cd backend && cp config/config.example.yaml config/config.yaml && make run   # 后端 :8000
cd frontend && npm install && npm run dev                          # 前端 :3000
```

## 目录结构

```
backend/    Go 后端（cmd/ses-sender 入口；internal/{app,httpx,platform,六域}）
frontend/      Next.js 前端
mcp-server/    MCP server（Node）
deploy/        ECS 发布系统（make deploy ENV=prod TAG=vX.Y.Z）
docs/          v2/ 设计文档（需求→架构→模块→数据→API→引擎）；INFRA/DEPLOY/FEATURES/SECURITY 运维手册
migrations/    goose 数据库迁移（backend/migrations）
```

## 文档

- 设计文档：`docs/v2/`（01 需求 → 07 模块详细设计）
- 基础设施搭建：`docs/INFRA.md` ｜ 发布运维：`docs/DEPLOY.md` ｜ 安全清单：`docs/SECURITY.md`

## 环境变量 / 配置

配置三级合并：命令行 flag > 环境变量 > `config.yaml` > 内置默认值。模板见 `backend/config/config.example.yaml`；密钥生产环境走 SSM/环境变量注入，不落文件。

## 开发

```bash
cd backend
make lint && make test && make build    # CI 同款三连（.gitlab-ci.yml 自动执行）
```
