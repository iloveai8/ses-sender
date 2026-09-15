# ses-sender 基础建设篇（INFRA）——AWS 底座从零搭建

> **分支说明（v2）**：本文基线为 Python v0.1.5 生产。v2 分支后端已切换为 Go（`backend-go/`），但 AWS 基础设施（VPC/ALB/ECS/SSM/RDS/SQS/SNS）与命名契约完全沿用——本文全部基建描述继续有效。

> **定位**：按**终态架构**直线重建一遍全部 AWS 基础设施。
> 每个资源先讲**是什么、没有它会怎样**（具体故障症状），再讲怎么建——第一遍就能读懂。
> **配套**：`DEPLOY.md`（部署实施篇）——发布系统、首次上线、日常发布、故障速查。
> **适用**：① 复盘学习 ② 将来建 test 环境（命名表 prod→test 全表照抄）。
> **基线**：2026-09-10 生产实测全绿（登录全链路 200/JWT）。

---

## 总路线图（八个阶段，先看依赖再动手）

```
阶段一 IAM 权限体系 ──────────┐   ┌──────────────────────────────┐
阶段二 ECR 镜像仓 ×2 ─────────┤   │ 依赖要点：                    │
阶段三 SSM 参数 + RDS ────────┼──►│ · 目标组引用 VPC              │
阶段四 安全组 ×3 ─────────────┤   │ · ALB 规则引用目标组           │
阶段五 ALB + 目标组 ×2 ────────┤   │ · 命名空间是 Service Connect  │
阶段六 Cloud Map 命名空间 ─────┤   │   的地基（部署脚本引用）       │
阶段七 CloudWatch 日志组 ×2 ───┘   │ · 日志组不建，任务起不来       │
        │                          └──────────────────────────────┘
        ▼
   基建就绪 ──► 转 DEPLOY.md「首次上线」
```

**一句话导览**（先把全局装进脑子）：

| 你将来天天打交道的 | 它们合起来干的事 |
|---|---|
| ALB + 目标组 + 安全组 | 让公网用户稳定地访问到"每次发布都换 IP"的任务 |
| ECR + SSM + RDS | 存镜像、存密钥、存数据——代码之外的三样家当 |
| IAM（用户/角色/策略） | 谁能干什么：部署的人、跑任务的容器，各拿各的钥匙 |
| Cloud Map 命名空间 + 日志组 | 前端找后端的内网通道 + 出事时看日志的地方 |

---

## 0. 终态架构与资源全景

### 0.1 终态流量图（这张图是整个项目的"地图"，先看懂它）

```
═══════════════ 公网（互联网）═══════════════
   浏览器·管理员/用户                邮件收件人（外部陌生人）
       │                               │ 点击邮件里的链接
       │ 页面 + /api/* 一切请求          │ GET /unsubscribe?token=xx
       ▼                               │ GET /uploads/xx.png
   ┌──────────── ALB :80（唯一公网入口）◄┘
   │
   ├─ 规则① /unsubscribe* ──────────► tg-backend-prod ──► 后端:8000 ┐
   ├─ 规则② /uploads/* ────────────► tg-backend-prod ──► 后端:8000 ┘← 仅有的两扇
   │                                                                   对外小窗(只读)
   └─ 默认（其余一切请求）──────────► tg-frontend-prod
                                        │
                                   前端任务 :3000（Next.js）
                                        │ /api/* → 剥前缀 → 反代
                                        ▼
                                  http://backend ★短名
                                        │
                                  ①前端任务内的 SC代理接管
                                        │ 集群内网直达（SG 只认 task 组）
                                        ▼
                                  ②后端任务内的 SC代理
                                        ▼
                                   后端 :8000 ────► RDS
```

**"后端不对公网"的精确含义**：后端不当门面、API 不对外——公网摸后端任意其他路径
（/admin、/auth/login 等）都落到前端变 404；能进后端的只有退订/图片两扇只读小窗
（业务上天生对外：印在已发出的邮件里）。

### 0.2 资源全景清单（真实 ID 备忘，免翻控制台）

| 资源 | 名称 / ID | 关键参数 |
|------|----------|---------|
| 账号 / 区域 | 303235427801 / us-east-2 | |
| VPC | vpc-0c757575b5e1ff7cb (Jackpotland-ehe-vpc) | |
| 子网（任务用） | subnet-093711efd84b2c92c (2a) + subnet-095bc84475c1622b2 (2b) | 公有子网，出网走 IGW |
| 子网（ALB 用） | 上面两个 + subnet-0c32c26aadb2f417b (2c) | 三个全勾更稳 |
| ALB | ses-sender-alb-prod | DNS: `ses-sender-alb-prod-521135262.us-east-2.elb.amazonaws.com` |
| 目标组（后端） | ses-sender-tg-backend-prod | HTTP:8000，健康检查 `/` 30s 阈值2/3 |
| 目标组（前端） | ses-sender-tg-frontend-prod | HTTP:3000，健康检查 `/` 30s 阈值2/3 |
| 安全组 ALB | sg-014d2c850acd9a695 | 入站 80/443 公网 |
| 安全组 task | sg-0292253bdd161911c | 入站 8000←[ALB组+自己]、3000←ALB组、80←自己 |
| 安全组 RDS | sg-0f45bb54dc695e1bf | 入站 3306←task组（+临时本机IP白名单） |
| IAM 角色（执行） | ses-sender-prod-execution | 拉镜像/写日志/取SSM |
| IAM 角色（任务） | ses-sender-prod-task | 容器内调 SES/SQS/S3/Bedrock |
| IAM 部署用户 | TimeCapsule-jackpotlandslots-ses | 6 条托管 + 1 条 AWS 托管 |
| SSM 参数 | /ses-sender/prod/{SECRET_KEY, DATABASE_URL} | 均 SecureString |
| RDS | ses-sender-prod（MySQL 8, db.t4g.small, gp3 20G） | 端点 `.cedgd7shvvve.us-east-2.rds.amazonaws.com`，库 ses_sender，用户 ses_app |
| ECR | gs/ses-sender/backend、gs/ses-sender/frontend | 私有 |
| Cloud Map 命名空间 | ses-sender-prod.local（ns-suh7ocwelu4qfz6p） | 专用(Private)，VPC 同上 |
| 日志组 | /ecs/ses-sender/{backend,frontend}-prod | 各保留 7 天 |
| ECS 集群/服务 | ses-sender-prod / backend + frontend | 由 DEPLOY.md 流程创建 |

---

## 1. 前置条件

| 项 | 值 / 说明 |
|----|----------|
| AWS 账号 | 团队共享账号（有多项目资源，命名务必带前缀隔离） |
| 区域 | us-east-2（SES 身份所在区，控制台右上角确认） |
| VPC | 团队既有 VPC（含 ≥2 个公有子网，路由表含 IGW——Fargate 免 NAT 出网） |
| 本机工具 | Docker Desktop、AWS CLI v2、Git Bash（Windows 三件套） |

**Windows 三坑速记**（每个 Git Bash 终端开工前）：

```bash
export MSYS_NO_PATHCONV=1   # 坑1：/ses-sender/... 会被翻译成 C:/Git/ses-sender/...，API 直接报参数非法
export PYTHONUTF8=1         # 坑2：aws 输出含特殊字符(✓/⨯)时 GBK 编码崩溃
export PATH="$PATH:/c/Program Files/Amazon/AWSCLIV2"   # 坑3：新终端可能找不到 aws
# 坑4（防患）：aws --query 的键名只用英文——中文键在 Windows 下会乱码导致 jmespath 报错
# 坑5（防患）：Makefile 命令必须单行写——Windows GNU Make 的多行续行会断字符串
```

---

## 2. 阶段一：IAM 权限体系

### ◆ 这一步在解决什么问题

AWS 里**一切 API 调用都要凭证**，而凭证背后是"权限清单"（策略）。这一步回答三个问题：

1. **谁来发布？** → 部署用户（一把长期密钥放本地 .env，CI 将来也用它）
2. **云端任务用什么身份跑？** → 两个角色（执行角色=平台拉镜像/读密钥；任务角色=容器内调 SES）
3. **每份权限给多少？** → 最小化：只给用到的动作（收紧版连"删服务"都故意不给——部署密钥删不了线上，是安全设计）

**◆ 没有它会怎样（对照症状）**

| 缺什么 | 具体症状（实测出现过） |
|--------|----------------------|
| 缺 ecs:CreateService | `make deploy` 第一步就 AccessDenied |
| 缺 elasticloadbalancing:DescribeTargetGroups | deploy.sh 查目标组被拒（当初可视化建策略漏勾，实测踩过） |
| 缺 iam:PassRole | 建服务时报 "not authorized to perform iam:PassRole" |
| 缺 servicediscovery:GetNamespace | 建 Service Connect 服务报 NamespaceNotFoundException（**误导性报错**，看似"命名空间不存在"，实为读权限缺失） |
| 任务没挂执行角色 | 任务卡在 PROVISIONING（拉不了镜像） |
| 任务没挂任务角色 | 容器内调 SES 报 `Unable to locate credentials` |

### 2.1 权限体系图（谁拿什么凭证干什么）

```
┌─ 本机 / CI ────────────────────────────────┐
│  部署用户 TimeCapsule-jackpotlandslots-ses  │
│  （密钥放本地 .env，gitignored）            │
│    ├─ gs-ecr                     推/拉镜像  │
│    ├─ gs-logs-read               查日志    │
│    ├─ gs-servicediscovery        SC 校验   │
│    ├─ ses-sender-ecs-deploy      建服务    │
│    ├─ ses-sender-iam-edit        挂角色策略 │
│    ├─ ses-sender-sg-write        改 task SG│
│    └─ AmazonSESFullAccess        本地发信  │
└────────────────────────────────────────────┘
┌─ 云端任务运行时（无密钥！）─────────────────┐
│  执行角色 ses-sender-prod-execution         │
│    （ECS 平台用：拉镜像/写日志/读 SSM）     │
│  任务角色 ses-sender-prod-task              │
│    （容器代码用：SES/SQS/CloudWatch/Bedrock/S3）│
└────────────────────────────────────────────┘
```

**核心安全设计**：云上任务**不持有任何密钥文件**——调 AWS 用任务角色（临时凭证自动轮换），
读数据库密码/签名密钥用 SSM 注入。密钥只存在两处：SSM（加密）和本地 .env（gitignored）。

### 2.2 七条策略（JSON 全文）

**创建方式统一**：IAM → 左侧「策略」→「创建策略」→ JSON 页粘贴 → 命名 → 创建 →
回用户页「添加权限→直接附加策略」挂上。

> 三条铁律（全部实测踩过）：
> ① **内联策略总和 2048 字符硬限**——早期一条大内联就顶满，再加任何权限报
>    "超出 2048 非空格字符限制"。**一律走托管**（单条 6144、跨用户复用、不占内联额度）。
> ② **内联不能原地改名/转托管**——唯一路径 = 新建托管 → 挂上 → 再删内联（顺序反了会权限空窗）。
> ③ **命名跟着管辖范围走**：`gs-*` 团队复用（别的项目部署用户直接挂），`ses-sender-*`
>    项目专属（策略里写死了本项目的角色/资源 ARN，别的项目用了会被拒——预期行为）。

**① gs-ecr（团队 · ECR 读写，gs/ 全部仓）**

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "EcrLogin",
      "Effect": "Allow",
      "Action": ["ecr:GetAuthorizationToken"],
      "Resource": "*"
    },
    {
      "Sid": "GsPushPull",
      "Effect": "Allow",
      "Action": [
        "ecr:BatchCheckLayerAvailability",
        "ecr:BatchGetImage",
        "ecr:BatchDeleteImage",
        "ecr:CompleteLayerUpload",
        "ecr:DescribeImages",
        "ecr:DescribeRepositories",
        "ecr:GetDownloadUrlForLayer",
        "ecr:InitiateLayerUpload",
        "ecr:PutImage",
        "ecr:UploadLayerPart"
      ],
      "Resource": "arn:aws:ecr:us-east-2:303235427801:repository/gs/*"
    }
  ]
}
```

> 为什么 Resource 是 `gs/*`：早期只授权单仓 backend ARN，建 frontend 仓后立刻
> AccessDenied——改成前缀通配，一次到位，将来 mcp 等新仓免再改。

**② gs-logs-read（团队 · 日志只读）**

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": [
      "logs:GetLogEvents",
      "logs:FilterLogEvents",
      "logs:DescribeLogStreams",
      "logs:DescribeLogGroups"
    ],
    "Resource": "*"
  }]
}
```

**③ gs-servicediscovery（团队 · Service Connect 校验+实例查询）**

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": [
      "servicediscovery:GetNamespace",
      "servicediscovery:ListNamespaces",
      "servicediscovery:GetService",
      "servicediscovery:ListServices",
      "servicediscovery:CreateService",
      "servicediscovery:ListInstances",
      "servicediscovery:DiscoverInstance"
    ],
    "Resource": "*"
  }]
}
```

**④ ses-sender-ecs-deploy（项目 · ECS 部署最小集）**

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "EcsDeploy",
      "Effect": "Allow",
      "Action": [
        "ecs:CreateCluster",
        "ecs:DescribeClusters",
        "ecs:RegisterTaskDefinition",
        "ecs:DescribeTaskDefinition",
        "ecs:CreateService",
        "ecs:UpdateService",
        "ecs:DescribeServices",
        "ecs:ListTasks",
        "ecs:DescribeTasks"
      ],
      "Resource": "*"
    },
    {
      "Sid": "DiscoverInfra",
      "Effect": "Allow",
      "Action": [
        "ec2:DescribeSecurityGroups",
        "elasticloadbalancing:DescribeTargetGroups",
        "elasticloadbalancing:DescribeLoadBalancers",
        "elasticloadbalancing:DescribeListeners",
        "elasticloadbalancing:DescribeRules",
        "elasticloadbalancing:DescribeTargetHealth",
        "sqs:GetQueueUrl"
      ],
      "Resource": "*"
    },
    {
      "Sid": "PassDeployRoles",
      "Effect": "Allow",
      "Action": "iam:PassRole",
      "Resource": [
        "arn:aws:iam::303235427801:role/ses-sender-prod-execution",
        "arn:aws:iam::303235427801:role/ses-sender-prod-task"
      ]
    }
  ]
}
```

> **iam:PassRole 是什么**：建服务时要"把角色递给 ECS"（任务以该身份跑），这个递的动作
> 就是 PassRole——只允许递本项目两个角色，别的项目递不了。
> **故意不含** DeleteService/DeleteCluster/StartTask 等（可视化编辑器时代混进来的杂项也
> 一并清了）——删服务走控制台，部署密钥删不了线上 = 安全冗余。

**⑤ ses-sender-iam-edit（项目 · 挂角色策略用）**

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": ["iam:PutRolePolicy", "iam:GetRolePolicy"],
    "Resource": [
      "arn:aws:iam::303235427801:role/ses-sender-prod-task",
      "arn:aws:iam::303235427801:role/ses-sender-prod-execution"
    ]
  }]
}
```

**⑥ ses-sender-sg-write（项目 · 只能改 task 安全组的入站规则）**

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": [
      "ec2:AuthorizeSecurityGroupIngress",
      "ec2:RevokeSecurityGroupIngress"
    ],
    "Resource": "arn:aws:ec2:us-east-2:303235427801:security-group/sg-0292253bdd161911c"
  }]
}
```

> 为什么需要它：控制台编辑安全组规则曾反复"保存不了"（草稿陷阱），有这条后
> `aws ec2 authorize-security-group-ingress` 一条命令直达，不再看控制台脸色。

**⑦ AmazonSESFullAccess（AWS 托管 · 本地开发后端发测试邮件）**
本地 `.env` 用的就是部署用户密钥，本地调试发信靠它；云端发信走任务角色不经它。**保留勿删。**

### 2.2.1 废弃归档：ci-ecr-push-policy.json（2026-09-10 删除，内容存档于此）

**身世**：9月7日后端单服务时代规划"CI 用独立密钥推镜像"准备的策略文件，从未挂载到任何
用户/角色（孤儿）。授权范围只含 backend 单仓——前端仓上线后即使挂上也推不了。
**被谁取代**：部署用户的 `gs-ecr`（覆盖 gs/* 双仓+查删，见 2.2①）；未来 CI 也定调
"部署密钥即 CI 密钥"（DEPLOY.md 7.3 路线#7）。**原文件全文存档**：

```json
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "StsIdentity",
            "Effect": "Allow",
            "Action": ["sts:GetCallerIdentity"],
            "Resource": "*"
        },
        {
            "Sid": "EcrAuth",
            "Effect": "Allow",
            "Action": ["ecr:GetAuthorizationToken"],
            "Resource": "*"
        },
        {
            "Sid": "EcrPushSesSenderBackend",
            "Effect": "Allow",
            "Action": [
                "ecr:DescribeRepositories",
                "ecr:CreateRepository",
                "ecr:BatchCheckLayerAvailability",
                "ecr:GetDownloadUrlForLayer",
                "ecr:BatchGetImage",
                "ecr:PutImage",
                "ecr:InitiateLayerUpload",
                "ecr:UploadLayerPart",
                "ecr:CompleteLayerUpload",
                "ecr:ListImages",
                "ecr:DescribeImages"
            ],
            "Resource": "arn:aws:ecr:us-east-2:303235427801:repository/gs/ses-sender/backend"
        }
    ]
}
```

> 存档它有两个知识点值得留：① `sts:GetCallerIdentity` 是**免授权动作**——任何有效签名
> 的请求都能调（`aws sts get-caller-identity` 永远可用于验证"我现在的凭证是谁"），所以
> 现行策略里不需要它；② `ecr:CreateRepository` 允许"仓库不存在则自动建"——CI 自动化
> 常用，但人肉流程建议显式建仓（避免拼错名静默建出野仓库）。

### 2.3 两个任务角色（先建"裸角色"，策略命令行挂）

```
IAM → 角色 →「创建角色」
  → 可信实体：AWS 服务 → 搜索 Elastic Container Service
  → 选「Elastic Container Service Task」   ← 必须 Task 用例（自动生成 ecs-tasks 信任）
角色 ses-sender-prod-execution：权限页勾托管策略 AmazonECSTaskExecutionRolePolicy → 创建
角色 ses-sender-prod-task：权限页什么都不勾 → 直接创建（裸角色）
```

> 为什么策略走 CLI 挂：角色策略可视化编辑器对 `sesv2:*` 报红线、Resource 清成空串会
> 整语句报错——**编辑器的坑，不是策略问题**：
>
> ```bash
> cd deploy/iam
> aws iam put-role-policy --role-name ses-sender-prod-task \
>   --policy-name ses-sender-task-policy \
>   --policy-document file://task-role-policy-prod.json && echo "task挂载成功"
> aws iam put-role-policy --role-name ses-sender-prod-execution \
>   --policy-name ses-sender-ssm-read \
>   --policy-document file://task-execution-policy.json && echo "execution挂载成功"
> ```
>
> 两份 JSON 以仓库 `deploy/iam/` 为权威（文件=源码，角色=运行态；改文件不生效，要重跑 put-role-policy 同步）：
>
> | 文件 | 挂到谁 | 给谁的权力 |
> |------|--------|-----------|
> | `task-role-policy-prod.json` | 任务角色 | **容器代码**调 AWS：SES v1+v2 双前缀全权（**为什么双前缀**：代码 v1/v2 API 都在用——v1 直发、v2 发送引擎/模板，前缀不可互换）+ SQS 锁队列 + CloudWatch 三查询 + Bedrock + S3 锁桶 |
> | `task-execution-policy.json` | 执行角色 | **ECS 平台**启动时读密钥：仅 `ssm:GetParameters` 限 `/ses-sender/*`（拉镜像/写日志的权力来自控制台勾的 AWS 托管策略 AmazonECSTaskExecutionRolePolicy，不在这份文件里） |

### 2.4 验收

```bash
aws ecr describe-repositories --repository-names gs/ses-sender/backend --region us-east-2 \
  --query 'repositories[0].repositoryUri' --output text          # gs-ecr（建仓后）
aws ecs describe-clusters --cluster ses-sender-prod --region us-east-2 \
  --query 'clusters[0].status' --output text                      # ecs-deploy（建集群后）
aws servicediscovery get-namespace --id ns-suh7ocwelu4qf6p --region us-east-2 \
  --query 'Namespace.Name' --output text                          # servicediscovery（建命名空间后）
aws iam get-role-policy --role-name ses-sender-prod-task --policy-name ses-sender-task-policy | head -15
```

---

## 3. 阶段二：ECR 镜像仓 ×2

### ◆ 它是什么 / 没有它会怎样

**私有 Docker 镜像仓库**——`docker push/pull` 的远端。Fargate 起任务时从这拉镜像。

**没有它会怎样**：本地 build 出的镜像没地方放；Fargate 任务卡死在拉镜像环节
（`CannotPullContainerError`），服务永远起不来。

```
ECR → 存储库 →「创建存储库」
  可见性: 私有
  名称:   gs/ses-sender/backend      （再建一个 gs/ses-sender/frontend）
  其他:   全默认
```

> 坑：ECR 是**全项目唯一带 `gs/` 前缀的资源**（仓库名含斜杠是 ECR 的"路径"特性），
> 其他资源一律不带——曾把日志组建成 /ecs/gs/... 只能删了重建。
> 验收：`aws ecr describe-repositories --repository-names gs/ses-sender/frontend --region us-east-2`

---

## 4. 阶段三：SSM 参数 + RDS

### ◆ 它们是什么 / 没有会怎样

**SSM 参数** = 云端的加密保险柜，存两样东西：`SECRET_KEY`（登录 token 签名密钥，泄露=
可伪造任意登录态）、`DATABASE_URL`（数据库连接串）。容器启动时由 ECS 注入为环境变量——
**这就是"云端版的 backend/.env"**。

**没有它会怎样**：任务启动即崩（拿不到 DATABASE_URL 连不上库；拿不到 SECRET_KEY
登录接口 500）。

### 4.1 密钥注入链路图

```
控制台手写一次 ──► SSM /ses-sender/prod/SECRET_KEY     ┐
                  SSM /ses-sender/prod/DATABASE_URL     ├─ 任务定义 secrets 段声明 valueFrom
                                                          │ 容器启动时 ECS 取值注入环境变量
RDS 建好后回填 DATABASE_URL ────────────────────────────┘  config.py 读取 = 云端版 .env
```

```
生成密钥：python -c "import secrets; print(secrets.token_hex(32))"
入口：控制台搜 Systems Manager → Parameter Store
  （首页的"启用新体验"/Quick Setup 按钮全部无视——那是管 EC2 服务器群的，Fargate 用不到）
  /ses-sender/prod/SECRET_KEY     SecureString  KMS=aws/ssm(默认免费)
  /ses-sender/prod/DATABASE_URL   SecureString  建好 RDS 后回填
```

> 坑：①类型必须 **SecureString**（默认是明文 String），建错类型不能原地改（删了重建）；
> ②Standard 层免费，别选 Advanced($0.05/月/个)；③参数本身免费开箱即用，无需"启动"什么。

### 4.2 RDS

**◆ 是什么 / 没有会怎样**：托管的 MySQL。没有它→数据没地方放（Fargate 任务磁盘是
**临时的**，每次发布重置，绝不能把数据库放容器本地盘）。

```
RDS（控制台搜"Aurora and RDS"）→ 创建数据库
  创建方式: 标准创建              ← 两个卡片别选成 Aurora Serverless（另一个产品，更贵）
  引擎: MySQL 8 | 模板: 生产
  标识符:   ses-sender-prod
  凭证:     自我管理（别选 Secrets Manager，收费且与 SSM 方案重复）
            主用户名 ses_app / 密码只用字母+数字（含 @:/ 拼 URL 要转义，坑）
  实例:     可突发性能类 → db.t4g.small（ARM 省20%；与镜像 x86 无关——DB 走网络协议）
  存储:     20GiB gp3（与 gp2 同价基准更高）
  连接:     VPC=Jackpotland / 公开访问=否 / SG=ses-sender-rds-prod / 初始库名 ses_sender
  加密:     可不开；开就用默认 aws/rds（别新建 KMS，$1/月）
→ 约 10 分钟 Available，终端节点回填进 SSM 的 DATABASE_URL：
  mysql+pymysql://ses_app:密码@ses-sender-prod.xxxx.us-east-2.rds.amazonaws.com:3306/ses_sender
```

> 本地导数据：临时开"公开访问"+RDS SG 加本机 IP 白名单（曾用 98.98.125.109/32），
> mysqldump 导入，**用完删白名单**。密码永不进 git/文档。

---

## 5. 阶段四：安全组 ×3

### ◆ 它是什么 / 没有会怎样

**实例级防火墙**——每个网卡（ENI）必须挂一个，规则=「允许谁从哪个端口进」。
本项目三道门：ALB 的门、任务边界的大门、数据库的门。

**没有/配错会怎样（每条规则都有实测尸检）**：

| 规则缺失 | 实测症状 |
|---------|---------|
| 8000←ALB组（**生命线**） | ALB 健康检查被挡 → 目标转 unhealthy → **ECS 直接杀任务**（事件 `Task failed ELB health checks`），换新任务继续死，循环到服务躺平（running=0） |
| 8000←task组 | 前端 SC 代理送不到后端 → 登录等一切 /api 500（ECONNRESET） |
| 3000←ALB组 | 前端目标组永不 healthy → 页面 503 |
| 80←task组 | SC 别名通道不通 |
| 多开 8080←0.0.0.0/0 | 后端 API 裸奔公网（曾划的红线，庆幸从未踩） |

### 5.1 终态规则总表

| 组 | 名称 | 入站规则 |
|----|------|---------|
| ALB | ses-sender-alb-prod (sg-014d2c850acd9a695) | 80←0.0.0.0/0；443←0.0.0.0/0（留 HTTPS） |
| task | ses-sender-task-prod (sg-0292253bdd161911c) | **8000←[ALB组+自己]**；3000←ALB组；80←自己 |
| rds | ses-sender-rds-prod (sg-0f45bb54dc695e1bf) | 3306←task组（+临时本机IP） |

### 5.2 task 组四条路各自服务谁（理解了就永远不会删错）

```
ALB ──(8000:健康检查+退订/图片流量)──► 后端任务   ← 8000←ALB组
前端任务SC代理 ──(8000:服务间调用)──► 后端任务   ← 8000←task组(自引用)
ALB ──(3000:页面+/api入口)────────► 前端任务    ← 3000←ALB组
前端SC代理 ──(80:SC别名监听)──────► 后端任务    ← 80←task组
```

> 三个实测坑：
> ① **同端口多来源合并显示**——8000 的两个来源在 API 里是一个数组；查询只看
>   `UserIdGroupPairs[0]` 会误判"规则丢失"，冤枉同事没保存（真事）。校验必须取全量：
>   `--query 'IpPermissions[].{port:FromPort,src:UserIdGroupPairs[].GroupId}'`
> ② 规则**描述只能 ASCII**（中文报 Invalid rule description）。
> ③ 出站保持默认全放行（任务要拉镜像/调 AWS/SC 注册心跳）。

---

## 6. 阶段五：目标组 ×2 + ALB + 监听器规则

### ◆ 它们是什么 / 没有会怎样

**目标组 = 派单名单**：记着"哪些任务 IP:端口 在服务 + 各自健康与否"。**为什么必须有它**：
Fargate 任务每次发布都换 IP，不可能把 IP 写死在任何地方——总得有个东西实时跟踪，
这就是目标组（ECS 发布时自动换名单，人只建组不填人）。

**没有它会怎样**：ALB 收到请求不知道发给谁（503）；ECS 服务没有挂 LB 的载体；
健康检查没有执行者——坏任务永远不会被摘流量。

**ALB = 分拣员+唯一门牌**：用户只记它的域名；按规则把请求派给目标组。

**没有 ALB 会怎样**：用户访问谁？（任务 IP 随机变）；两条邮件裸路径怎么分流到后端？

### 6.1 创建流程图

```
建 tg-backend-prod(8000) ─┐
建 tg-frontend-prod(3000) ─┼──► 建 ALB(:80) ──► 加两条路径规则 ──► 完工
                          │     （默认规则此时指谁都不影响生产，
                          │        首次部署前的初始态而已）
```

```
目标组（EC2 → 目标组 → 创建；菜单位置在"负载均衡"分组下，较深）：
  目标类型: IP 地址（Fargate 必选——任务没有 EC2 实例，只有 ENI IP）
  ses-sender-tg-backend-prod  HTTP:8000   ┐ 健康检查统一：
  ses-sender-tg-frontend-prod HTTP:3000   ┘ 协议HTTP 路径/ 间隔30s 超时5s 阈值 2/3
  注册目标页【什么都不填】直接创建 ← 手滑填 IP = 留一条永远不健康的死目标（实测坑）

ALB（EC2 → 负载均衡器 → 创建 → Application Load Balancer）：
  名称 ses-sender-alb-prod / 方案 面向互联网 / IPv4 / VPC Jackpotland
  子网: 三个全勾（093711...2a / 095bc8...2b / 0c32c2...2c，以表单实际为准）
  安全组: 从现有选择 → 只勾 ses-sender-alb-prod
  监听器: HTTP:80 → 转发至 ses-sender-tg-frontend-prod
```

> 坑：目标组**端口必须等于容器监听端口**（后端 8000/前端 3000）。曾把后端组建到 8080——
> 8080 是"监听器端口"的门牌，不是容器门牌；建错不可改只能删了重建。
> ALB 创建时控制台若提供"创建新安全组"默认项——删掉，只挂现有 ses-sender-alb-prod。

### 6.2 终态监听器规则（:80 上共 3 条）

| 优先级 | 条件 | 动作 | 服务对象 |
|--------|------|------|---------|
| 1 | 路径 `/unsubscribe*` | → tg-backend-prod | 邮件退订链接（裸路径直达后端） |
| 2 | 路径 `/uploads*` | → tg-backend-prod | 邮件内图片 |
| 默认 | 其余一切 | → tg-frontend-prod | 页面 + /api/*（前端是唯一门面） |

```
加规则：ALB → 选中 →「监听器和规则」→ :80 行 → 查看/编辑规则 → 添加规则
  条件: 路径 /unsubscribe*  操作: 转发至 ses-sender-tg-backend-prod  优先级 1
  条件: 路径 /uploads*      操作: 转发至 ses-sender-tg-backend-prod  优先级 2
```

> 顺序敏感（鸡蛋问题）：ECS 挂目标组建服务时，**要求该组已被某条监听器规则引用**——
> 所以规则要先建（哪怕默认规则暂时指向它），服务才建得起来。
> 历史注记：曾存在 ":8080 内网监听器"方案，实测 IGW 回环故障废弃删除（复盘见 DEPLOY.md 附录）。

---

## 7. 阶段六：Cloud Map 命名空间（Service Connect 地基）

### ◆ 它是什么 / 没有会怎样

**服务互访的"姓氏"**。Service Connect 让前端用 `http://backend` 这个短名调后端，
这个名字登记在命名空间里（全名 backend.ses-sender-prod.local）。

**没有它会怎样**：SC 服务配置无处安家 → 服务间只能退回"硬编码 IP"（每次发布失效）或
"多买内网 ALB"（$16/月）。

```
控制台顶部搜 "Cloud Map" → 左侧「命名空间」→ 右上「创建命名空间」
  类型: 【专用 Private】        ← 别选公共(互联网可见)/HTTP(无DNS，代码用不了名字)
  名称: ses-sender-prod.local   ← .local 结尾表私有；含环境名
  VPC:  Jackpotland-ehe-vpc
  TTL:  默认（15~60 秒均可——SC 路由由代理实时负责，TTL 只是兜底）
→ 创建后记录 ns- ID（当前 ns-suh7ocwelu4qf6p）
```

> 两个坑：① **别用团队共享命名空间**（已存在的 jackpotland）——名字泛（backend.jackpotland
> 谁都能撞）、不分环境；项目+环境自成姓氏。② ECS 集群页的「命名空间」标签页可能没有
> 创建按钮（控制台版本差异），Cloud Map 服务主页入口稳定。

---

## 8. 阶段七：CloudWatch 日志组 ×2

### ◆ 它是什么 / 没有会怎样

**日志的收纳盒**。任务定义指定"日志写进哪个组"，Fargate 的日志驱动要求组**预先存在**。

**没有它会怎样**：任务直接起不来（不是"没日志看"这么温柔，是任务无法启动）。

```
CloudWatch → 日志 → 日志组 → 创建
  /ecs/ses-sender/backend-prod    保留 1 周
  /ecs/ses-sender/frontend-prod   保留 1 周
```

> 坑：**不带 gs/ 前缀**；不可改名只能删了重建；必须在首发前建好。

---

## 附录A：命名契约总表（换环境 prod→test 全表照抄）

| 资源 | 命名 | 当前值/ID |
|------|------|----------|
| ECR（唯一带 gs/） | gs/ses-sender/{backend,frontend} | ✅ |
| 安全组 | ses-sender-{alb,task,rds}-prod | sg-014d…/sg-0292…/sg-0f45… |
| 目标组 | ses-sender-tg-{backend,frontend}-prod | ✅ |
| 负载均衡器 | ses-sender-alb-prod | ✅ |
| IAM 角色 | ses-sender-prod-{execution,task} | ✅ |
| 日志组 | /ecs/ses-sender/{backend,frontend}-prod | ✅ |
| SSM 参数 | /ses-sender/prod/{SECRET_KEY,DATABASE_URL} | ✅ |
| 命名空间 | ses-sender-prod.local | ns-suh7ocwelu4qf6p |
| 集群（自动建） | ses-sender-prod | ✅ |
| ECS 服务 | backend / frontend | ✅ |
| 任务家族 | ses-sender-{backend,frontend}-prod | ✅ |
| RDS | ses-sender-prod | ✅ |
| 部署策略（用户级） | gs-* 团队 / ses-sender-* 项目 | 6+1 条 |

## 附录B：概念速查（大白话，一表带走）

| 概念 | 一句话理解 | 没有它会怎样 |
|------|-----------|-------------|
| 集群 | 逻辑空壳，免费，装服务的筐 | 服务没地方放（但建集群零成本，脚本自动建） |
| 服务 | "保持 N 个任务在跑"的守护者 | 任务死了没人重启；**loadBalancers/SC 配置创建后不可改**，要换只能删了重建 |
| 任务定义 | 一次发布的"容器清单"快照，每次注册产生新 revision | 没有发布历史/不可回溯；revision 即版本号 |
| 任务 | 真正跑容器的实例，**每次发布换新 IP** | —（这是"为什么需要目标组"的根源） |
| 目标组 | 派单名单：任务IP+健康状态，ECS 发布自动换新 | ALB 不知道发给谁（503）；坏任务不会被摘流量 |
| ALB | 分拣员+唯一稳定门牌（域名） | 用户没有稳定入口；无法按路径分流 |
| 监听器/规则 | ALB 的门（端口）+ 分拣逻辑（路径→转发） | 一切流量混在一起，无法"页面给前端/裸路径给后端" |
| 安全组 | 实例级防火墙，规则="允许谁从哪个端口进" | 该通的没通=超时/被杀；不该通的通了=裸奔 |
| 执行角色 | ECS 平台的身份（拉镜像/写日志/读SSM） | 任务卡 PROVISIONING |
| 任务角色 | 容器内代码的身份（调 SES/SQS/S3） | 调 AWS 报 Unable to locate credentials |
| Service Connect | 服务间内网通道：每任务一个代理容器（体检/重试/分流） | 退回硬编码 IP 或买内网 ALB；滚动交接期有错误窗口 |
| Cloud Map 命名空间 | 服务互访名字的"姓氏" | SC 配置无处安家 |
| 100/200 滚动 | 先起新→健康→才停旧 | —（无状态服务用它=零停机） |
| 0/100 滚动 | 先停旧→再起新 | —（防后台线程双跑：后端用，代价 1~3 分钟闪断） |
| 部署断路器 | 新版本起不来自动回滚上一版 | 坏版本一直循环尝试（好在还有手动回滚） |
| 内联 vs 托管策略 | 纹身 vs 衣服（额度 2048 总和 vs 6144/条可复用） | 内联加满后任何新权限都加不进（实测） |
