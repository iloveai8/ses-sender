# ses-sender 部署实施篇（DEPLOY）——发布系统与上线运维

> **分支说明（v2）**：本文基线为 Python v0.1.5 生产。v2 分支后端已切换为 Go（`backend-go/`），taskdef 健康检查已改为 `ses-sender healthcheck`；文中 Python 专属命令（alembic/pytest/pip）不再适用（Go 等价见 `backend-go/Makefile`）。ECS 发布系统、灰度策略、排障手册继续有效。

> **定位**：仓库发布系统怎么运作、首次怎么上线、日常怎么发布、出事怎么排障。
> **配套**：`INFRA.md`（基础建设篇）——基建不齐先看那篇。
> **读者**：复盘 / 接手发布的人。**基线**：2026-09-10 生产实测全绿。

---

## 总路线图（一张图看懂发布系统全貌）

```
                     ┌─────────── 日常发布（第4章）───────────┐
                     │                                        │
改代码 → git commit → git tag vX.Y.Z ──► 三选一敲发布命令：
                                          │
                                          ├─ make deploy ENV=prod TAG=vX.Y.Z PART=frontend
                                          │    （只发前端：100/200 滚动=先起新·健康·才停旧→零停机）
                                          ├─ make deploy ENV=prod TAG=vX.Y.Z PART=backend
                                          │    （只发后端：0/100 滚动=先停旧·再起新→停1~3分钟挑低峰）
                                          └─ make deploy ENV=prod TAG=vX.Y.Z
                                               （不传 PART = 全量，前后端都发）
                              ┌───────────┴───────────┐
                              │ ①构建  ②推送  ③deploy.sh│
                              └───────────┬───────────┘
                                          ▼
        预检镜像已在ECR → 按名字现场查四样基建的真实ID（零配置文件）：
          安全组SG(门卫，查GroupId) / 目标组TG(派单名单，查ARN) / ALB(查DNS域名) / 命名空间
        → 渲染注册任务定义
        → 幂等建集群 → 首建/滚动更新服务(挂目标组+Service Connect) → 等稳定 → 完成

首次上线（第3章）= 同一条命令，服务不存在时自动走"首建"分支
```

---

## 0. 发布系统设计原则

| 原则 | 落地方式 |
|------|---------|
| **tag 驱动** | 版本号=git tag=镜像 tag=任务定义 revision 的唯一来源；严格校验（v主.次.修订 + tag 已登记），不猜测无默认 |
| **一条命令** | `make deploy` 包办构建/推送/建服务；首次自动建设，之后自动滚动 |
| **按需发布** | `PART=backend\|frontend\|all`——只改前端就只发前端（前端零停机） |
| **零配置文件** | 施工参数（SG/TG/ALB/命名空间）运行时按命名约定查 AWS，仓库不存基础设施细节 |
| **12-factor** | 同一镜像，环境差异全靠环境变量/SSM 注入；密钥不落盘不进 git |
| **最小权限** | 部署密钥删不了线上（无 DeleteService）；收紧版策略只含用到的动作 |

---

## 1. 仓库资产四件套（`deploy/` 目录）

```
deploy/
├── Makefile                       发布入口（人/CI 都只跟它打交道）
├── scripts/
│   ├── deploy.sh                  发布主脚本（5 步流水线）
│   ├── taskdef.py                 填表员：模板+参数→任务定义JSON，防呆校验
│   ├── taskdef-template.json      后端任务定义模板（{{占位符}}）
│   └── taskdef-frontend-template.json  前端任务定义模板
├── iam/                           任务角色/执行角色的策略 JSON（权威）
│   ├── task-role-policy-prod.json
│   └── task-execution-policy.json
└── .gitattributes                 LF 锁定（防 Windows CRLF 破坏脚本）
```

### 1.1 Makefile（全文）

```makefile
# ses-sender 前后端构建/发布入口（GitLab CI / Jenkins 只是本文件的包装器）
# 环境与发布范围都作为参数显式传入（防止误操作；test 当前休眠保留）
# 用法示例（TAG 严格校验：须显式传 + v主.次.修订 + git tag 已登记）:
#   make build TAG=v0.1.0                    # 构建后端镜像（自动清本地旧镜像）
#   make build-frontend TAG=v0.1.0           # 构建前端镜像
#   make push TAG=v0.1.0                     # 登录 ECR 并推后端镜像
#   make push-frontend TAG=v0.1.0            # 登录 ECR 并推前端镜像
#   make deploy ENV=prod TAG=v0.1.0                      # 全量发布（前后端，prod 交互确认）
#   make deploy ENV=prod TAG=v0.1.0 PART=backend         # 只发后端（0/100 滚动，停 1~3 分钟，挑低峰）
#   make deploy ENV=prod TAG=v0.1.0 PART=frontend        # 只发前端（100/200 滚动，零停机）
#   make images / make rmi TAG=v0.1.0        # 查看 / 删除远程后端镜像

AWS_ACCOUNT ?= 303235427801
AWS_REGION  ?= us-east-2
ECR_REPO    ?= gs/ses-sender/backend
ECR_REPO_FE ?= gs/ses-sender/frontend
ECR_URI     := $(AWS_ACCOUNT).dkr.ecr.$(AWS_REGION).amazonaws.com/$(ECR_REPO)
ECR_URI_FE  := $(AWS_ACCOUNT).dkr.ecr.$(AWS_REGION).amazonaws.com/$(ECR_REPO_FE)
ROOT        := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))/..)
SCRIPTS     := $(ROOT)/deploy/scripts
TAG         :=
IMAGE       := $(ECR_URI):$(TAG)
IMAGE_FE    := $(ECR_URI_FE):$(TAG)
ENV         :=
PART        ?= all

# 环境合法性检查：必须显式传且只能是 prod / test（test 休眠保留中）
check-env:
	@if [ -z "$(ENV)" ]; then echo "ERROR: ENV is required, e.g. make deploy ENV=prod TAG=v0.1.0"; exit 1; fi
	@echo "$(ENV)" | grep -qE "^(test|prod)$$" || { echo "[make] ENV must be test or prod, got: $(ENV)"; exit 1; }

# PART 合法性检查：空=全量 all；显式传则必须是 backend|frontend|all
check-part:
	@echo "$(PART)" | grep -qE "^(backend|frontend|all)$$" || { echo "[make] PART must be backend|frontend|all (empty=all), got: $(PART)"; exit 1; }

# TAG 严格校验（不猜测、无默认值）
check-tag-set:
	@if [ "$(origin TAG)" != "command line" ]; then echo "ERROR: TAG is required, e.g. make build TAG=v0.1.0"; exit 1; fi

check-tag-format: check-tag-set
	@echo "$(TAG)" | grep -qE "^v[0-9]+\.[0-9]+\.[0-9]+$$" || { echo "ERROR: TAG must be v MAJOR.MINOR.PATCH (e.g. v0.1.0), got: $(TAG)"; exit 1; }

check-tag-version: check-tag-format
	@git rev-parse -q --verify "refs/tags/$(TAG)" >/dev/null || { echo "ERROR: git tag $(TAG) not found. Create it first: git tag $(TAG) && git push origin $(TAG)"; exit 1; }

.PHONY: build build-frontend login push push-frontend deploy images rmi check-env check-part check-tag-set check-tag-format check-tag-version

# build：必须是已登记的 git tag；构建前删本地旧镜像，保证本地只有最新一份
build: check-tag-version
	@ids=$$(docker images -q $(ECR_URI)); if [ -n "$$ids" ]; then docker rmi -f $$ids >/dev/null 2>&1; echo "[build] removed old local backend image(s) of $(ECR_REPO)"; else echo "[build] no old backend image"; fi
	docker build -t $(IMAGE) $(ROOT)/backend

build-frontend: check-tag-version
	@ids=$$(docker images -q $(ECR_URI_FE)); if [ -n "$$ids" ]; then docker rmi -f $$ids >/dev/null 2>&1; echo "[build] removed old local frontend image(s) of $(ECR_REPO_FE)"; else echo "[build] no old frontend image"; fi
	docker build -t $(IMAGE_FE) $(ROOT)/frontend

login:
	aws ecr get-login-password --region $(AWS_REGION) | \
		docker login --username AWS --password-stdin \
		$(AWS_ACCOUNT).dkr.ecr.$(AWS_REGION).amazonaws.com

push: check-tag-version build login
	docker push $(IMAGE)

push-frontend: check-tag-version build-frontend login
	docker push $(IMAGE_FE)

# deploy：按 PART 决定构建推送哪个镜像，再调 deploy.sh（prod 交互确认——单行写法，Windows GNU Make 多行续行会断字符串）
deploy: check-env check-part check-tag-version
	@if [ "$(PART)" = "frontend" ] || [ "$(PART)" = "all" ]; then $(MAKE) push-frontend TAG=$(TAG); fi
	@if [ "$(PART)" = "backend" ] || [ "$(PART)" = "all" ]; then $(MAKE) push TAG=$(TAG); fi
	@if [ "$(ENV)" = "prod" ]; then echo "Deploying PROD [$(PART)]: $(TAG)"; read -p "Type yes to continue: " c; [ "$$c" = "yes" ] || { echo "Cancelled"; exit 1; }; fi
	bash $(SCRIPTS)/deploy.sh $(ENV) $(TAG) $(PART)

images:
	aws ecr describe-images --repository-name $(ECR_REPO) --region $(AWS_REGION) --output table

# 删除远程 ECR 镜像 tag：make rmi TAG=v0.0.2（必须显式传且为 v主.次.修订 格式；仅后端仓）
rmi: check-tag-format
	aws ecr batch-delete-image --repository-name $(ECR_REPO) --region $(AWS_REGION) --image-ids imageTag=$(TAG)
	@echo "deleted remote backend tag: $(TAG)"
```

### 1.2 deploy.sh 五步流水线（结构讲解，全文以仓库为权威）

```
第0步 预检    镜像必须已在 ECR（describe-images 查 tag）——晚失败不如早失败
第1步 查参数  按命名约定运行时查询：task安全组/双目标组/ALB域名/SQS(可选)
              查不到=基建没建齐 → 当场报错并指向命名表，绝不带病继续
第2步 幂等建集群（存在就跳过）
第3步 后端    渲染 taskdef-template → 注册(产生新revision) → 服务存在则滚动更新，
              不存在则首建（挂 tg-backend + Service Connect 服务端配置）
第4步 前端    渲染 taskdef-frontend-template → 注册 → 首建/滚动更新
              （挂 tg-frontend + SC 纯客户端配置，BACKEND_URL=http://backend 短名）
第5步 等稳定  services-stable 阻塞到 期望副本=运行副本（首拉镜像 2~4 分钟）
```

关键设计片段（为什么这么写）：

```bash
# ① 三重保险头：-e 失败即止 / -u 未定义变量报错 / -o pipefail 管道任一环失败即整体失败
set -euo pipefail

# ② Windows 兼容三件套（每处都有血泪史）
export MSYS_NO_PATHCONV=1            # 防路径翻译
SCRIPT_DIR_W=$(cygpath -m "${SCRIPT_DIR}" 2>/dev/null || echo "${SCRIPT_DIR}")
TASKDEF_JSON="$SCRIPT_DIR_W/.taskdef.tmp.json"   # 临时文件放脚本目录（/tmp 两家解析不一致）

# ③ 服务存在性判断必须【精确等于】ACTIVE——
#    已删服务的墓碑态是 INACTIVE，grep ACTIVE 会子串误命中 → 误走更新分支（实测踩过）
SVC_STATE=$(aws ecs describe-services ... --query 'services[0].status' --output text) || SVC_STATE=""
if [ "${SVC_STATE}" = "ACTIVE" ]; then ... 更新分支 ... else ... 首建分支 ... fi

# ④ 两套滚动策略（哲学不同，见第4章）
DEPLOY_CFG   ="minimumHealthyPercent=0,maximumPercent=100,..."    # 后端：先停旧再起新
DEPLOY_CFG_FE="minimumHealthyPercent=100,maximumPercent=200,..."  # 前端：先起新健康再停旧

# ⑤ Service Connect（首建时挂，创建后不可改——这是服务只能删了重建的根因之一）
# 后端（服务端）：
--service-connect-configuration "enabled=true,namespace=${NS_ID},services=[{portName=app,discoveryName=backend,clientAliases=[{port=80,dnsName=backend}]}]"
# 前端（纯客户端）：
--service-connect-configuration "enabled=true,namespace=${NS_ID}"
# ★NS_ID 必须传完整 ARN——传短 ns- ID 会报 NamespaceNotFoundException（实测）

# ⑥ 命名空间 ARN 硬编码（同 SUBNETS 先例，账号级稳定资源）
NS_ID=arn:aws:servicediscovery:us-east-2:303235427801:namespace/ns-suh7ocwelu4qfz6p
```

### 1.3 任务定义模板 ×2（JSON 全文）

**后端 `taskdef-template.json`**（要点：端口命名 app 供 SC 引用；SSM 注入双密钥；
健康检查必须 CMD 数组格式）：

```json
{
    "family": "{{FAMILY}}",
    "networkMode": "awsvpc",
    "requiresCompatibilities": ["FARGATE"],
    "cpu": "512",
    "memory": "1024",
    "executionRoleArn": "{{EXECUTION_ROLE_ARN}}",
    "taskRoleArn": "{{TASK_ROLE_ARN}}",
    "runtimePlatform": {"cpuArchitecture": "X86_64", "operatingSystemFamily": "LINUX"},
    "containerDefinitions": [{
        "name": "backend",
        "image": "{{IMAGE}}",
        "essential": true,
        "portMappings": [{"name": "app", "containerPort": 8000, "protocol": "tcp"}],
        "environment": [
            {"name": "AWS_REGION", "value": "us-east-2"},
            {"name": "ENABLE_SENDER", "value": "true"},
            {"name": "SENDER_CONCURRENCY", "value": "2"},
            {"name": "SENDER_MESSAGE_RATE", "value": "0"},
            {"name": "SQS_QUEUE_URL", "value": "{{SQS_QUEUE_URL}}"},
            {"name": "UNSUBSCRIBE_BASE_URL", "value": "{{UNSUBSCRIBE_BASE_URL}}"},
            {"name": "BEDROCK_MODEL_ID", "value": "global.anthropic.claude-opus-4-6-v1"},
            {"name": "BEDROCK_REGION", "value": "us-east-1"}
        ],
        "secrets": [
            {"name": "SECRET_KEY", "valueFrom": "{{SSM_PREFIX}}/SECRET_KEY"},
            {"name": "DATABASE_URL", "valueFrom": "{{SSM_PREFIX}}/DATABASE_URL"}
        ],
        "logConfiguration": {
            "logDriver": "awslogs",
            "options": {"awslogs-group": "{{LOG_GROUP}}", "awslogs-region": "us-east-2", "awslogs-stream-prefix": "backend"}
        },
        "healthCheck": {
            "command": ["CMD", "python", "-c",
              "import urllib.request as u, sys; sys.exit(0 if u.urlopen('http://localhost:8000/', timeout=3).status == 200 else 1)"],
            "interval": 30, "timeout": 5, "retries": 3, "startPeriod": 60
        }
    }]
}
```

> 健康检查的血泪史：CMD-SHLEX（字符串）对带引号长命令解析出错报——任务明明 curl 200
> 却被判 UNHEALTHY 杀掉（v0.0.1 rev:1 之死）。**必须 CMD 数组格式**。
> 两套健康检查别混淆：任务定义的 healthCheck（ECS 杀任务用）vs 目标组的健康检查（ALB 摘流量用）。
> 容量决策（2026-09-10 实测降档）：CloudWatch 7天数据显示内存峰值仅 7.2%（145MB/2GB）、
> CPU 峰值 23.5%，1vCPU/2GB → **512CPU/1GB**（月省 $18）。复核入口：ECS→服务详情→「指标」。

**前端 `taskdef-frontend-template.json`**（无密钥无任务角色；wget 健康检查）：

```json
{
    "family": "{{FAMILY}}",
    "networkMode": "awsvpc",
    "requiresCompatibilities": ["FARGATE"],
    "cpu": "512",
    "memory": "1024",
    "executionRoleArn": "{{EXECUTION_ROLE_ARN}}",
    "runtimePlatform": {"cpuArchitecture": "X86_64", "operatingSystemFamily": "LINUX"},
    "containerDefinitions": [{
        "name": "frontend",
        "image": "{{IMAGE}}",
        "essential": true,
        "portMappings": [{"containerPort": 3000, "protocol": "tcp"}],
        "environment": [{"name": "BACKEND_URL", "value": "{{BACKEND_URL}}"}],
        "logConfiguration": {
            "logDriver": "awslogs",
            "options": {"awslogs-group": "{{LOG_GROUP}}", "awslogs-region": "us-east-2", "awslogs-stream-prefix": "frontend"}
        },
        "healthCheck": {
            "command": ["CMD", "wget", "-q", "-O", "/dev/null", "http://localhost:3000/"],
            "interval": 30, "timeout": 5, "retries": 3, "startPeriod": 60
        }
    }]
}
```

> 渲染工具 `taskdef.py` = 填表员：`--image` 填 `{{IMAGE}}`、`--set KEY=VALUE` 填任意占位符；
> 两道防呆：有空位没填 → 拒绝输出；填完不是合法 JSON → 拒绝输出。

### 1.4 参数总表（config.py 全量 12 个 + 前端 2 个）

| 分类 | 参数 | 当前值(prod) | 作用 / 不设会怎样 |
|------|------|-------------|------------------|
| 安全 | `SECRET_KEY` | SSM 注入 | JWT 签名密钥；用默认值=可伪造登录态 |
| 连接 | `DATABASE_URL` | SSM 注入 | 连哪个库；错=启动即崩 |
| AWS | `AWS_REGION` | us-east-2 | SES/SQS/Bedrock 区域（与默认一致，双保险） |
| 开关 | `ENABLE_SENDER` | true | 发送引擎线程；false=纯API实例（**将来 api/worker 拆分的开关**） |
| 开关 | `SQS_QUEUE_URL` | ses-sender-events-prod 队列URL | 回执统计。**deploy.sh 按命名约定 `ses-sender-events-{env}` 自动发现**：查到注入/查不到传空（休眠，不算错） |
| 开关 | `UNSUBSCRIBE_BASE_URL` | ALB域名 | 退订链接前缀；空=邮件里印"#"（合规缺失）；上域名后切 https://域名 |
| 开关 | `SES_CONFIGURATION_SET` | ses-sender-events-prod | 事件发布总开关，**同上自动发现**（2026-09-14 开通）。链路=配置集→SNS→SQS→Worker回写库，$0/月。**故意不配 CloudWatch 事件目标**：按 batch_id 维度会累积自定义指标（$0.30/个/月，越多批次越贵），批次指标已改查数据库 |
| 引擎 | `SENDER_CONCURRENCY` | 2 | 并发工人数；量大可升 4~8（内存余量充足） |
| 引擎 | `SENDER_MESSAGE_RATE` | 0 | 每工人每秒上限；**0=自动跟随 SES 配额** |
| 引擎 | `SENDER_SLIDING_WINDOW_SECONDS` | 0 | 滑动窗口秒数；撞配额报错时与下行配对启用（如60/300） |
| 引擎 | `SENDER_SLIDING_WINDOW_RATE` | 0 | 窗口内总量上限 |
| AI | `BEDROCK_MODEL_ID` / `BEDROCK_REGION` | opus-4-6 / us-east-1 | 模板AI优化用的模型与调用区 |
| 前端 | `BACKEND_URL` | http://backend（SC短名） | 反代目标；兜底 http://backend:8000（compose用） |
| 前端 | `NEXT_PUBLIC_API_URL` | **未设** | **休眠后门：设了=浏览器直连后端，永远别设** |

> 另有写死项：登录令牌有效期 24 小时（config.py `ACCESS_TOKEN_EXPIRE_HOURS`，改需代码）。
> 本地开发额外两个：`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`（backend/.env，boto3 凭证链
> 直读；云端不需要——任务角色自动授予）。

---

## 2. Service Connect 配置详解

### ◆ 它解决什么问题 / 没有它会怎样

前端容器要调后端 API（反代），但**后端任务 IP 每次发布都变**——前端怎么找到它？

| 方案 | 结局 |
|------|------|
| 硬编码 IP | 发布即失效 ❌ |
| 借公网 ALB 的 :8080 | **IGW 回环故障**（DNS 给公网 IP→出网折返→源NAT→安全组引用失效→连接超时）❌ 实测废弃 |
| 买内部 ALB | 可行，+$16/月 |
| **Service Connect ✅** | 免费；每任务一个代理容器：体检/自动重试/分流 |

### 2.1 SC 数据流图

```
前端容器 fetch("http://backend/auth/login")     ★必须短名，不带端口(=80)
   │ ① 名字由【本任务内】的 SC代理 编程解析（不建任何 DNS 区记录！
   │    用全名 backend.ses-sender-prod.local 反而 ENOTFOUND——实测坑）
   ▼
本任务 SC代理（快递员）
   │ ② 查 Cloud Map 配送名单：backend = 10.113.x.x:8000（任务起落自动登记/注销）
   │ ③ 集群内网直连后端任务（过安全组：8000←task组）+ 主动体检 + 失败自动重试
   ▼
后端任务 SC代理（监听别名端口80）→ 转发给同任务的 FastAPI:8000
```

### 2.2 配置速查（deploy.sh 里已内置，此处供理解）

| 项 | 值 | 说明 |
|---|---|---|
| 命名空间 | ses-sender-prod.local（传**完整 ARN**） | 短 ID 会报 NamespaceNotFoundException |
| 后端 SC | portName=app, discoveryName=backend, clientAlias={port:80, dnsName:backend} | app=模板端口名；8000=真实容器端口 |
| 前端 SC | enabled + namespace（纯客户端） | 无 services 数组 |
| 前端反代目标 | `http://backend` | **短名**。FQDN 不解析 |
| 代理容器资源 | 每任务预留约 128CPU/64MB | 现规格够，不需升配 |
| 配套 SG | 8000←task组（自引用）+ 80←task组 | 代理送货走的路（详见 INFRA 第5章） |

### 2.3 回执事件链路（SES→SNS→SQS→库，2026-09-14 开通，$0/月）

> 送达/打开/点击等指标的数据来源。**故意不配 CloudWatch 事件目标**——按 batch_id 维度会累积自定义指标（$0.30/个/月），指标一律查库。

**资源与命名约定**（deploy.sh 按名自动发现，查不到=传空休眠）：

| 资源 | 名字 | 说明 |
|------|------|------|
| SES 配置集 | `ses-sender-events-{env}` | 事件发布总开关；事件目标=SNS，7 种事件全开（SEND/DELIVERY/BOUNCE/COMPLAINT/OPEN/CLICK/REJECT） |
| SNS 主题 | `ses-sender-events-{env}` | 中转站 |
| SQS 标准队列 | `ses-sender-events-{env}` | VisibilityTimeout=60；订阅开 **RawMessageDelivery**（Worker 两种格式都兼容） |

**两段关键策略（CLI 手工建的必配，控制台向导建会自动加——实测三个坑全在这层）**：

```json
// ① SNS 主题策略追加：允许 SES 服务主体发布（缺它=SES 事件被主题静默丢弃）
{ "Sid": "AllowSESPublish", "Effect": "Allow",
  "Principal": { "Service": "ses.amazonaws.com" },
  "Action": "SNS:Publish",
  "Resource": "arn:aws:sns:us-east-2:<账号>:ses-sender-events-prod",
  "Condition": { "StringEquals": { "AWS:SourceAccount": "<账号>" } } }

// ② SQS 队列策略：Principal 必须是 "*"（不能写账号 root！SNS 投递用服务身份，
//    写 root=每条投递 AccessDenied——SNS 指标表现为 Delivered=0/Failed=N）
{ "Sid": "AllowSNSTopicPublish", "Effect": "Allow",
  "Principal": { "AWS": "*" },
  "Action": "sqs:SendMessage",
  "Resource": "arn:aws:sqs:us-east-2:<账号>:ses-sender-events-prod",
  "Condition": { "ArnEquals": { "aws:SourceArn": "arn:aws:sns:us-east-2:<账号>:ses-sender-events-prod" } } }
```

**排障入口**：链路断时按 SNS 主题指标三分（`AWS/SNS` 命名空间，TopicName 维度）：
`Published 有量 + Delivered=0` = 上面两段策略问题；`Published=0` = 发送没带配置集（查任务定义环境变量 + Scanner 日志 `config_set=`）。

**施工策略摘除警告**：`gs-event-pipeline-build`（deploy/iam/ 同名 JSON）用完可从部署密钥摘除，**但摘前必须给部署密钥补 `sqs:GetQueueUrl`**——deploy.sh 每次发版按名查队列注入 `SQS_QUEUE_URL`，没有该权限会查到空 → **回执统计静默失效**（症状=新批次全 0）。

**Worker 已知行为**：非 JSON 消息处理失败即删除（防毒消息循环重试，v0.1.5 起生效）。

---

## 3. 首次上线（从零到全绿的直线流程）

> 前提：INFRA.md 八阶段全验收 + 本仓库代码已就位。服务未对外时无需顾忌停机窗口。

```
① 打版本号
   git tag v0.0.1 && git push origin dev v0.0.1

② 一条命令（首建集群+双服务，约 8~12 分钟：本地双镜像构建→推送→注册→建服务→等稳定）
   cd deploy && make deploy ENV=prod TAG=v0.0.1

③ 六项验收（全绿才算上线成功）
   ┌────────────────────────────────────────────────────┐
   │ 1 双目标组 healthy（backend+frontend）              │
   │ 2 双服务任务 RUNNING&HEALTHY（含 SC 代理容器）       │
   │ 3 页面：浏览器开 ALB 域名 → 登录页正常               │
   │ 4 ★登录全链路：POST /api/auth/login → 200+token     │
   │ 5 退订走线：GET /unsubscribe?token=test → 后端提示页 │
   │ 6 前后端日志组均有输出、无 ConnectTimeout/ENOTFOUND  │
   └────────────────────────────────────────────────────┘

④ 验收命令速用：
   aws elbv2 describe-target-health --target-group-arn <TG_ARN> \
     --region us-east-2 --query 'TargetHealthDescriptions[].TargetHealth.State'
   curl -s -X POST http://<ALB>/api/auth/login -H 'Content-Type: application/json' \
     -d '{"username":"admin","password":"***"}'
```

> 两个顺序敏感点（首上线的"鸡蛋问题"）：
> ① 目标组必须先被监听器规则引用，ECS 才能挂它建服务——规则先建。
> ② Service Connect/负载均衡配置**创建后不可修改**——要改只能删服务重跑 deploy
>   （脚本自动走"首建"分支，这正是它保留首建逻辑的原因）。

---

## 4. 日常发布手册（建成后每天用的就这些）

### 4.1 PART 决策图

```
这次改了什么？
   ├─ 只改前端（页面/样式/前端逻辑）──────► PART=frontend   （零停机，随时发）
   ├─ 只改后端（API/发信/定时）──────────► PART=backend    （停1~3分钟，挑低峰）
   ├─ 前后端都改 / 大版本 ───────────────► 不传 PART=all
   └─ 拿不准 ──────────────────────────► 发全量，永远安全
```

```bash
git add <文件> && git commit -m "feat[xxx]:描述"
git tag v0.0.4 && git push origin dev v0.0.4
cd deploy
make deploy ENV=prod TAG=v0.0.4 PART=frontend   # prod 会交互输 yes 确认
```

### 4.2 前端为什么零停机（100/200 滚动时间线）

> **数字含义**：这是 ECS 部署策略的两个百分比（相对期望副本数，本项目=1）——
> `最低健康%`：滚动过程中旧任务至少保留多少健康才允许停 / `最高允许%`：临时最多跑几个任务。
> 前端 100/200 = "至少保 1 个健康、最多临时双开"→ 先起新、健康、才停旧；后端 0/100 见 4.3。

```
T0  新前端任务启动
T1  容器健康检查过 → 注册目标组
T2  ALB 体检 2 连过(约60秒) → 标记 healthy → 开始接流量 ★此刻新旧双活
T3  ECS 停旧任务 → 旧目标除名 → 新请求全走新任务
T4  旧连接排空（默认300秒宽限）→ 在途请求体面完成
全程任意时刻：名单上至少一个健康目标 → 用户无感
```

### 4.3 后端为什么必须停 1~3 分钟（0/100）

后端任务里有**后台线程**（Sender 发信引擎 + 30秒定时器）。新旧任务并存 = 同一批
定时邮件发两遍（业务事故）——宁可先停旧再起新。**挑低峰发布即无感**；根治靠
api/worker 拆分（见第 7 章路线）。

### 4.4 发布前检查单

```
□ git tag 已登记且推远程（make 会校验，但提前想好版本号）
□ ECR 里有同名镜像冲突？（换新 tag 就不会有）
□ 后端发布：挑了低峰窗口？没有密集到点的定时任务？
□ 前端发布：任意时间可发
□ 发布后：六项验收第 4 项（登录）必测
```

---

## 5. 验收与监控

| 想看什么 | 命令 |
|---------|------|
| 目标组健康 | `aws elbv2 describe-target-health --target-group-arn <ARN> --region us-east-2 --query 'TargetHealthDescriptions[].TargetHealth.State'` |
| 服务任务数 | `aws ecs describe-services --cluster ses-sender-prod --services backend frontend --query 'services[].{name:serviceName,run:runningCount}' --output table` |
| 后端日志 | `aws logs tail /ecs/ses-sender/backend-prod --region us-east-2 --follow`（先 `export MSYS_NO_PATHCONV=1 PYTHONUTF8=1`） |
| 前端日志 | 同上换 frontend-prod |
| 页面探活 | `curl -s http://<ALB>/ \| head -c 80`（期望 Next.js HTML） |
| API 探活 | `curl POST /api/auth/login`（期望 200+token） |

**监控指标词汇表**（面板/排障用；指标名是各 AWS 服务的固定官方名，浏览树点开命名空间即可看到该服务全部指标）：

| 命名空间 | 指标 | 含义 | 建议统计 |
|---------|------|------|---------|
| AWS/SES（账号级） | Send / Deliveries / Bounces / Complaints / Rejects | 发送/送达/退信/投诉/拒绝 | **Sum**（数量类求和） |
| AWS/ECS | CPUUtilization / MemoryUtilization | 任务的 CPU/内存利用率 | Average |
| AWS/ApplicationELB | RequestCount / HTTPCode_Target_5XX_Count / HealthyHostCount | 请求数/后端5xx/健康目标数 | 前两 Sum、健康数 Average |
| AWS/RDS | DatabaseConnections / CPUUtilization / FreeableMemory | 连接数/CPU/可用内存 | Average |

> 控制面板 ses-sender：四层布局（业务SES→流量ALB→计算ECS→数据RDS），挂件全用线形图，
> 一个图可勾多个指标画多条线。自查入口：CloudWatch→控制面板→ses-sender。
> **坑（实测）**：SES 业务指标是"事件出生制"——零退信的账号里搜 Bounces 会搜不到
> （指标尚不存在，非操作错误）；首次真实群发后指标自动出现，届时再补挂件。

**回退手段**（无专门回滚命令，够用的两条路）：
- 发布后发现前端有问题：再发一版（零停机，几分钟）即可，无需回滚
- 后端起不来：部署断路器自动回滚上一 revision（deploymentCircuitBreaker 已内置）
- 服务彻底躺平（running=0）：`aws ecs update-service --cluster ses-sender-prod --service backend --force-new-deployment`

---

## 6. 故障速查大全（全部实战踩过，按四类归档）

### 6.1 构建/推送类（Windows 本机）

| 症状 | 原因 → 解法 |
|------|------------|
| 参数名被翻译成 `C:/Program Files/Git/...` | Git Bash 路径翻译 → `export MSYS_NO_PATHCONV=1` |
| aws 输出 `gbk codec can't encode` | 中文/特殊字符 + GBK 控制台 → `export PYTHONUTF8=1` |
| `--query` 报 jmespath 语法错 | 查询键用了中文（乱码）→ 键名只用英文 |
| Make 目标里字符串莫名断掉 | Windows GNU Make 多行续行坑 → 命令写单行 |
| 脚本 CRLF 报错 | Windows 换行符 → `deploy/.gitattributes` 锁 LF |
| `open //./pipe/dockerDesktopLinuxEngine 找不到` | Docker Desktop 掉线 → 启动它并 `docker info` 等 READY |
| mktemp 的 /tmp 文件 aws 读不到 | GitBash 与 Windows aws 对 /tmp 解析不一致 → 临时文件放脚本目录 |
| 新终端 `aws: command not found` | PATH → `export PATH=$PATH:/c/Program Files/Amazon/AWSCLIV2` |
| **Alembic 迁移"多个头"报错**（实测大坑） | 新迁移文件的 `down_revision` 必须挂在**当前链头**（用 `grep down_revision` 全目录排出真正的头），挂错节点 → 版本链分叉 → `multiple heads` 拒绝执行 → 列没建但代码已在查 → 引擎每 5 秒报 Unknown column。修法：改父版本重发版；排查入口：启动日志 `[Alembic] 迁移检查失败` |
| **监控时间戳要认时区**（实测大坑） | 本机 CLI 返回的时间戳自带 `+08:00`（北京时间）而非 `Z`（UTC）——误按 UTC 换算会把事件时间错 8 小时、查询窗口选错位置得出"查无数据" → 先看戳尾再对时间线 |
| ALB 流量突刺（几百请求/5分钟，几乎全 4xx） | 互联网扫描器探测公网 ALB（扫 /.env /wp-admin 等漏洞路径），**无害背景噪音**：被前端 404 全挡、后端零波及 → 判定法：请求全进前端TG + Target 4xx 占比>95% + 5xx=0；根治=域名后配 host-header 门禁（默认403） |

### 6.2 部署类（ECS）

| 症状 | 原因 → 解法 |
|------|------------|
| CreateService 报目标组"没有关联的负载均衡器" | 目标组还没被监听器规则引用 → 先建规则再跑部署 |
| 对已删服务 UpdateService 报 tg 无关联 LB | **INACTIVE 墓碑态被子串误命中**（grep ACTIVE 匹配了 INACTIVE）→ 判断精确等于 ACTIVE（已修） |
| 脚本报 `TASK_SG: unbound variable` | 变量定义顺序：NETWORK_CONFIG 引用 TASK_SG 却定义在查询前 → 挪到查询后（已修） |
| 任务起不来，日志组相关报错 | 日志组不存在 → 按 INFRA 第8章建（必须在首发前） |
| 任务反复被杀，事件 `failed container health checks` 但 curl 正常 | 健康检查 CMD-SHLEX 解析坑 → 必须 CMD 数组格式（v0.0.1 rev:1 之死） |
| CreateService 报 `NamespaceNotFoundException` | 双因：①部署密钥无 servicediscovery:GetNamespace（**误导性报错**）→ 加 gs-servicediscovery；②**必须传命名空间完整 ARN**，短 ns- ID 同报此错 |
| 服务 running=0 躺平 | TG 健康死亡循环后断路器放弃 → 修根因后 `update-service --force-new-deployment` |
| 目标组建错端口 | TG 端口=容器端口（8000/3000），不可改 → 删了重建 |
| 目标组里出现永不健康的假 IP | 建组时"注册目标"页手滑填了 IP → 删除该目标（Fargate 自动注册） |

### 6.3 网络类（SG/SC/流量）

| 症状 | 原因 → 解法 |
|------|------------|
| 前端日志 `ConnectTimeoutError ... 3.x.x.x:8080`（公网IP） | **IGW 回环**：公网 ALB 域名在任务侧解析出公网 IP→出网折返→源NAT→SG 引用失效 → 弃 8080 方案用 SC（见第7章复盘） |
| 前端日志 `getaddrinfo ENOTFOUND backend.ses-sender-prod.local` | **SC endpoint 必须短名**（代理只在任务内编程短名，不建 DNS 区记录）→ BACKEND_URL=http://backend |
| 登录 500 + `read ECONNRESET` | SC 代理直连后端 8000 被挡 → task SG 加 8000←task组（自引用） |
| 删了 8000←ALB 规则后任务被连环杀 | **该规则是生命线**：ALB 健康检查被挡→ECS 杀任务 → 补回（与自引用并存，双来源） |
| "规则明明加了却查不到" | 同端口多来源合并成数组，查询只取 `UserIdGroupPairs[0]` → 取全量 `[]` |
| 规则保存报 Invalid rule description | 描述含中文 → 只能 ASCII |
| 页面 503 / 目标组不健康 | 对应端口的 SG 来源没放行 ALB 组 → 对照 INFRA 5.2 四条路 |
| 公网 curl :8080 能通 | （历史）8080 来源错开成 0.0.0.0/0 → 立即改回 task 组（红线） |

### 6.4 权限类（IAM）

| 症状 | 原因 → 解法 |
|------|------------|
| 加内联策略报"超出 2048 非空格字符" | 内联总额度 → 一律走托管策略 |
| 想改策略名 | 托管/内联都不能原地改名 → 新建→挂→删旧 |
| 角色编辑器对 sesv2 报红线 | 编辑器坑非策略问题 → `aws iam put-role-policy` CLI 挂载 |
| 可视化建的部署策略漏 DescribeTargetGroups | 编辑器混入经典 ELB 动作 + 漏 v2 → 用完整 JSON 粘贴（INFRA 2.2④） |
| 编辑了策略验证仍被拒 | 没点完「保存更改」→ 走完保存，等 5 秒 |
| 新 ECR 仓 push AccessDenied | 单仓 ARN 授权 → Resource 改 `repository/gs/*` |
| `aws logs` AccessDenied | 无 logs 权限 → gs-logs-read |
| 策略列表"用作"列显示无 | 控制台索引滞后（已知毛病）→ 以 `aws logs describe-log-groups` 等实测为准 |
| 删托管策略删不掉 | 仍挂在实体上 → 先「分离」再删 |

---

## 7. 附录·架构演进复盘（时间线 + 教训集 + 未来路线）

### 7.1 演进时间线

```
2026-09-09  后端单服务首署 v0.0.1（0/100；rev:1 死于 CMD-SHLEX 健康检查坑，rev:2 活）
2026-09-09  决策：前后端分离双服务 + PART 独立发布
            9A 基建（ECR/TG×2/日志组/路径规则）+ IAM 重构（内联→托管×4）
2026-09-10  v0.0.3 双服务上线；:8080 反代方案遇 IGW 回环故障（API 全 500）
            ↓ 三方案对比：内部ALB($16/月) vs CloudMap(DNS缓存窗口) vs ServiceConnect
            选定 SC（免费+体检重试+扩展性）
            SC 落地五关：命名空间 → gs-servicediscovery 权限 → 必须传 ARN
                       → endpoint 必须短名 → SG 8000 双来源
            登录全链路 200/JWT ✅ → 清理 8080 遗留 + 旧目标组
            目录重构：deploy/ docs/ 上移项目根（前后端平级）
```

### 7.2 一句话教训集（每条都对应上面一次真实事故）

1. **文档说"应该"的，要实测**——"公网 ALB 在 VPC 内解析私有 IP"理论存在，实测给了公网 IP。
2. **报错信息会撒谎**——NamespaceNotFoundException 实为缺权限；"没保存"实为查询只取了数组[0]。
3. **端口三兄弟各司其职**——监听器端口（ALB的门）/ 目标组端口（派单用的容器门）/ 容器端口（真门），建错一个全链路不通。
4. **顺序敏感的鸡蛋问题要先画图**——"规则先引用目标组→才能建服务"这类依赖，动手指前在纸上过一遍。
5. **最小权限收紧后要全链路验收**——收紧删掉的权限，恰是下一步要用的（SC 需要 servicediscovery）。
6. **每跨一次任务边界都要过安检**——SC 代理的"魔法"只在起终点，中间跳依然走真实网卡。
7. **控制台草稿陷阱**——填了≠保存了；能用 CLI 的（SG 规则）就给密钥发把小刀。
8. **Windows 三件套是持久战**——MSYS/PYTHONUTF8/单行 Make，每个新终端都要念一遍经。

### 7.3 未来路线（已排优先级，未排期）

| # | 事项 | 价值 | 依赖 |
|---|------|------|------|
| 1 | ~~admin 改默认密码~~ | ✅ 2026-09-14 完成 | — |
| 2 | ~~域名 + HTTPS~~ | ✅ 2026-09-11 完成（ses-sender.luckystargame.com） | — |
| 3 | api/worker 服务拆分 | **后端发布也零停机**（api 100/200，worker 单副本）＋api 获得横向扩容资格 | 给 Scheduler 加开关（约3行代码，唯一代码改动）。⏭️ 用户决策暂缓（2026-09-14）：当前资源余量大（内存峰值<10%/CPU<25%），不够用时先纵向升规格 |
| 4 | ~~SQS 回执统计~~ | ✅ 2026-09-14 完成（见 §2.3，v0.1.4） | — |
| 5 | test 环境 | 预发布验证 | INFRA.md 命名表 prod→test 照抄 + ENV=test |
| 6 | ECR 镜像不可变性 | 防同 tag 覆盖 | 控制台开关 |
| 7 | CI（GitLab Runner 调 make） | 免手工发布 | 部署密钥即 CI 密钥，零新权限 |
