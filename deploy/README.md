# ses-sender 部署资产（前后端 · AWS ECS Fargate · Service Connect）

> tag 驱动发布：首次自动建设 cluster/service/task，之后按 PART 滚动更新（前端零停机）。
> **基建怎么建** → `docs/INFRA.md`；**发布怎么跑/怎么排障** → `docs/DEPLOY.md`。

## 用法（全部命令）

```bash
cd deploy

# 日常发布（先 git commit + git tag vX.Y.Z + push，再执行）
make deploy ENV=prod TAG=v0.0.4                      # 全量（前后端）
make deploy ENV=prod TAG=v0.0.4 PART=frontend        # 只发前端（100/200 滚动，零停机）
make deploy ENV=prod TAG=v0.0.4 PART=backend         # 只发后端（0/100 滚动，停1~3分钟挑低峰）

# 分步操作
make build          TAG=v0.1.0    # 构建后端镜像（须显式 tag；自动清本地旧镜像）
make build-frontend TAG=v0.1.0    # 构建前端镜像
make push           TAG=v0.1.0    # 构建 + 登录 ECR + 推送（须 git tag 已登记）
make push-frontend  TAG=v0.1.0    # 前端同款
make images                       # 查看远程 ECR 后端镜像列表
make rmi            TAG=v0.0.2    # 删除远程后端镜像 tag
```

TAG 规则：必须 `v主.次.修订` 格式且 git tag 已登记（build/push/deploy 共用校验，防伪版本号）。

## 目录

```
deploy/
├── Makefile                            # 命令入口（TAG/ENV/PART 严格校验）
├── .gitattributes                      # LF 锁定（防 Windows CRLF 破坏脚本）
├── scripts/
│   ├── deploy.sh                       # 发布主脚本（五步流水线，前后端+Service Connect）
│   ├── taskdef.py                      # 填表员：模板+--set → 任务定义 JSON（防呆校验）
│   ├── taskdef-template.json           # 后端任务定义模板
│   └── taskdef-frontend-template.json  # 前端任务定义模板
└── iam/                                # 任务角色策略 JSON（权威）
    ├── task-role-policy-prod.json / -test.json
    └── task-execution-policy.json
```

## 设计要点

- **零配置文件**：施工参数（安全组/目标组/ALB/命名空间）在 deploy.sh 里按命名约定
  **运行时查询 AWS API**（命名见 `docs/INFRA.md` 契约表）；仓库不存基础设施细节。
- **密钥不落盘**：SECRET_KEY/DATABASE_URL 在 SSM，任务启动时注入环境变量。
- **前端调后端**：Service Connect，endpoint 短名 `http://backend`（详见 `docs/DEPLOY.md` 第2章）。
- **部署密钥权限**：见 `docs/INFRA.md` 第2章（部署用户 7 条策略 JSON 全文）。
