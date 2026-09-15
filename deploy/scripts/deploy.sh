#!/usr/bin/env bash
# ============================================================================
# ses-sender 发布脚本（AWS ECS Fargate · 前后端双服务架构）
# ----------------------------------------------------------------------------
# 功能：按 git tag 发布。首次部署自动创建 集群/任务定义/服务（全套建设）；
#       之后每次部署自动滚动更新对应服务（前端零停机、后端防双跑）。
# 用法：deploy.sh <prod|test> <git-tag> [backend|frontend|all]
#       （日常入口是 Makefile：make deploy ENV=prod TAG=v0.1.0 PART=frontend）
# PART：backend  = 只发后端（0/100 滚动：先停旧再起新，防 Sender引擎/定时器双跑）
#       frontend = 只发前端（100/200 滚动：先起新健康再停旧，零停机）
#       all/留空 = 前后端都发
# 设计：施工参数（安全组/目标组/ALB等）全部按命名约定"运行时查询 AWS"获得，
#       仓库里零配置文件——控制台按 docs/INFRA.md 命名契约表建好资源即可直接部署。
# ============================================================================
set -euo pipefail
# ↑ 三重保险：-e 任何命令失败立即中止；-u 使用未定义变量报错；-o pipefail 管道任一环失败即整体失败
#   （防止"构建失败却继续推送旧镜像"这类假成功）

# ────────────────────────────── 入参 ──────────────────────────────
ENV_NAME=${1:?用法: deploy.sh <prod|test> <git-tag> [backend|frontend|all]}   # 第1参：目标环境
TAG=${2:?用法: deploy.sh <prod|test> <git-tag> [backend|frontend|all]}        # 第2参：版本号（=git tag=镜像 tag）
PART=${3:-all}                                                                # 第3参：发布哪部分，默认全量
case "${PART}" in
    backend|frontend|all) ;;
    *) echo "[deploy] PART 必须是 backend|frontend|all，收到: ${PART}" >&2; exit 1 ;;
esac

export MSYS_NO_PATHCONV=1   # GitBash防自动路径翻译:/ses-sender/...会被翻成C:/Git/...导致SSM参数名非法
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
SCRIPT_DIR_W=$(cygpath -m "${SCRIPT_DIR}" 2>/dev/null || echo "${SCRIPT_DIR}")  # Windows格式路径给python/aws
TPL="$SCRIPT_DIR_W/taskdef-template.json"                 # 后端任务定义模板（带 {{占位符}}）
TPL_FE="$SCRIPT_DIR_W/taskdef-frontend-template.json"     # 前端任务定义模板
TASKDEF_JSON="$SCRIPT_DIR_W/.taskdef.tmp.json"            # 渲染产物（不用/tmp: GitBash与Windows的aws对/tmp解析不一致）
trap 'rm -f "${TASKDEF_JSON}"' EXIT                       # 脚本退出时（无论成败）自动清理临时文件

# ─────────────────── 命名契约（改资源名 = 改这里）───────────────────
# 所有 AWS 资源名从 ENV_NAME 派生，这就是"换环境只改一个参数"的实现。
# 命名与 docs/INFRA.md 契约表一一对应，控制台建资源必须按这些名字。
AWS_REGION=us-east-2                                     # AWS 区域（SES/ECR 等资源所在区）
AWS_ACCOUNT=303235427801                                 # AWS 账号 ID
ECR_REPO=gs/ses-sender/backend                           # 后端镜像仓库名
ECR_REPO_FE=gs/ses-sender/frontend                       # 前端镜像仓库名
CLUSTER=ses-sender-${ENV_NAME}                           # 集群名（部署时不存在会自动创建）
SERVICE=backend                                          # 后端服务名（集群内唯一，首建后终身只更新）
SERVICE_FE=frontend                                      # 前端服务名
FAMILY=ses-sender-backend-${ENV_NAME}                    # 后端任务定义"家族名"（每次注册产生新 revision）
FAMILY_FE=ses-sender-frontend-${ENV_NAME}                # 前端任务定义家族名
TASK_SG_NAME=ses-sender-task-${ENV_NAME}                 # 容器安全组名（前后端共用，按名字查 ID）
TG_NAME=ses-sender-tg-backend-${ENV_NAME}                # 后端目标组（2026-09-09 由 tg-{env} 更名而来）
TG_NAME_FE=ses-sender-tg-frontend-${ENV_NAME}            # 前端目标组
ALB_NAME=ses-sender-alb-${ENV_NAME}                      # 负载均衡器名（按名字查 DNS 域名）
EXECUTION_ROLE_ARN=arn:aws:iam::${AWS_ACCOUNT}:role/ses-sender-${ENV_NAME}-execution
                                                         # 执行角色：ECS 平台用它拉镜像/写日志/取SSM密钥（前端也复用它）
TASK_ROLE_ARN=arn:aws:iam::${AWS_ACCOUNT}:role/ses-sender-${ENV_NAME}-task
                                                         # 任务角色：容器内代码用它调 SES/SQS/S3（仅后端需要）
SSM_PREFIX=/ses-sender/${ENV_NAME}                       # SSM 密钥路径前缀（SECRET_KEY/DATABASE_URL 在此之下）
LOG_GROUP=/ecs/ses-sender/backend-${ENV_NAME}            # 后端 CloudWatch 日志组名（必须预先存在）
LOG_GROUP_FE=/ecs/ses-sender/frontend-${ENV_NAME}        # 前端日志组名（必须预先存在）
SQS_NAME=ses-sender-events-${ENV_NAME}                   # SQS 队列名（可选：查不到就传空=暂无回执统计）
CONFIG_SET_NAME=ses-sender-events-${ENV_NAME}            # SES 配置集名（可选：事件发布开关，查不到就传空）
# 团队 VPC 的两个公有子网（Fargate 出网走 IGW，免 NAT 费用）
SUBNETS=subnet-093711efd84b2c92c,subnet-095bc84475c1622b2
# Cloud Map 命名空间（Service Connect 服务间互访的地基；详见 docs/DEPLOY.md 第2章）
# 注意：必须传完整 ARN——实测传短 ID 时 ECS CreateService 报 NamespaceNotFoundException
NS_ID=arn:aws:servicediscovery:us-east-2:303235427801:namespace/ns-suh7ocwelu4qfz6p
# 后端的 Service Connect 服务名：前端反代目标（别名端口80，故不带端口后缀）
# 注意：SC endpoint 用短名（代理在任务内编程解析，短名=clientAlias 的 dnsName）；
# 全名 backend.xxx.local 实测 ENOTFOUND（代理不编程 FQDN，且 Cloud Map 无 DNS 记录）
BACKEND_DNS=backend
# 对外正式域名（HTTPS）：退订链接/邮件资源的前缀；证书为 ACM *.luckystargame.com
SITE_DOMAIN=ses-sender.luckystargame.com
# SC 代理互访端口 = 客户端别名端口 80（task 安全组须有自引用放行 80）

IMAGE="${AWS_ACCOUNT}.dkr.ecr.${AWS_REGION}.amazonaws.com/${ECR_REPO}:${TAG}"       # 后端完整镜像地址
IMAGE_FE="${AWS_ACCOUNT}.dkr.ecr.${AWS_REGION}.amazonaws.com/${ECR_REPO_FE}:${TAG}" # 前端完整镜像地址
log()  { echo "[deploy][${ENV_NAME}][${PART}] $*"; }              # 统一日志前缀
die()  { echo "[deploy][${ENV_NAME}][${PART}] 错误: $*" >&2; exit 1; }   # 统一报错并退出

# 部署策略（两类服务的滚动哲学不同，原因见各自注释）：
#   后端 0/100：先停旧再启新——防两任务并存时 Sender引擎/定时器双跑重复发信
DEPLOY_CFG="minimumHealthyPercent=0,maximumPercent=100,deploymentCircuitBreaker={enable=true,rollback=true}"
#   前端 100/200：先启新（健康、注册目标组）再停旧——纯无状态 Web，可安全并存=零停机
DEPLOY_CFG_FE="minimumHealthyPercent=100,maximumPercent=200,deploymentCircuitBreaker={enable=true,rollback=true}"

# ════════════════ 第 0 步：预检——镜像必须已在 ECR ════════════════
# 防呆：没推镜像就来部署，会拖到服务启动才失败（晚失败不如早失败）
if [ "${PART}" = "backend" ] || [ "${PART}" = "all" ]; then
    aws ecr describe-images --repository-name "${ECR_REPO}" --image-ids imageTag="${TAG}" \
        --region "${AWS_REGION}" >/dev/null 2>&1 \
        || die "ECR 中不存在 ${ECR_REPO}:${TAG}，请先执行: make push TAG=${TAG}"
    log "后端镜像确认: ${IMAGE}"
fi
if [ "${PART}" = "frontend" ] || [ "${PART}" = "all" ]; then
    aws ecr describe-images --repository-name "${ECR_REPO_FE}" --image-ids imageTag="${TAG}" \
        --region "${AWS_REGION}" >/dev/null 2>&1 \
        || die "ECR 中不存在 ${ECR_REPO_FE}:${TAG}，请先执行: make push-frontend TAG=${TAG}"
    log "前端镜像确认: ${IMAGE_FE}"
fi

# ════════════════ 第 1 步：运行时查询施工参数（按命名约定）════════════════
# 查不到 = 资源没建或名字不符 → 当场报错并指向命名表，绝不带病继续
TASK_SG=$(aws ec2 describe-security-groups --region "${AWS_REGION}" \
    --filters "Name=group-name,Values=${TASK_SG_NAME}" \
    --query 'SecurityGroups[0].GroupId' --output text 2>/dev/null) \
    || TASK_SG=""
[ -n "${TASK_SG}" ] && [ "${TASK_SG}" != "None" ] || die "未找到安全组 ${TASK_SG_NAME}（按 docs/INFRA.md 命名契约表创建）"

# 服务网络配置（拼成 AWS 命令要求的格式，前后端两个服务复用）
# 注意：必须在上方 TASK_SG 查询成功之后再定义（提前定义会因 set -u 报未绑定变量——实测踩过）
NETWORK_CONFIG="awsvpcConfiguration={subnets=[${SUBNETS}],securityGroups=[${TASK_SG}],assignPublicIp=ENABLED}"
#                                                          ↑ 子网+安全组+自动公网IP（出网拉镜像/调AWS用）

ALB_DNS=$(aws elbv2 describe-load-balancers --region "${AWS_REGION}" \
    --names "${ALB_NAME}" --query 'LoadBalancers[0].DNSName' --output text 2>/dev/null) \
    || ALB_DNS=""
[ -n "${ALB_DNS}" ] && [ "${ALB_DNS}" != "None" ] || die "未找到负载均衡器 ${ALB_NAME}"
# ALB_DNS 用途：①前端容器的 BACKEND_URL（http://alb:8080 内网通道）②后端退订链接域名 ③收尾提示

if [ "${PART}" = "backend" ] || [ "${PART}" = "all" ]; then
    TG_ARN=$(aws elbv2 describe-target-groups --region "${AWS_REGION}" \
        --names "${TG_NAME}" --query 'TargetGroups[0].TargetGroupArn' --output text 2>/dev/null) \
        || TG_ARN=""
    [ -n "${TG_ARN}" ] && [ "${TG_ARN}" != "None" ] || die "未找到后端目标组 ${TG_NAME}"

    SQS_URL=$(aws sqs get-queue-url --region "${AWS_REGION}" --queue-name "${SQS_NAME}" \
        --query 'QueueUrl' --output text 2>/dev/null) || SQS_URL=""      # 队列是可选资源，查不到不算错
    [ "${SQS_URL}" = "None" ] && SQS_URL=""
    CONFIG_SET=$(aws sesv2 get-configuration-set --region "${AWS_REGION}" \
        --configuration-set-name "${CONFIG_SET_NAME}" \
        --query 'ConfigurationSetName' --output text 2>/dev/null) || CONFIG_SET=""
    [ "${CONFIG_SET}" = "None" ] && CONFIG_SET=""
    log "后端施工参数: TG=${TG_NAME} SQS=${SQS_URL:-未配置(跳过回执)} 配置集=${CONFIG_SET:-未配置(事件不发布)}"
fi

if [ "${PART}" = "frontend" ] || [ "${PART}" = "all" ]; then
    TG_ARN_FE=$(aws elbv2 describe-target-groups --region "${AWS_REGION}" \
        --names "${TG_NAME_FE}" --query 'TargetGroups[0].TargetGroupArn' --output text 2>/dev/null) \
        || TG_ARN_FE=""
    [ -n "${TG_ARN_FE}" ] && [ "${TG_ARN_FE}" != "None" ] || die "未找到前端目标组 ${TG_NAME_FE}"
    log "前端施工参数: TG=${TG_NAME_FE} BACKEND_URL=http://${BACKEND_DNS}（Service Connect）"
fi
log "共用施工参数: SG=${TASK_SG} ALB=${ALB_DNS}"

# ════════════════ 第 2 步：集群（幂等，前后端共用）═══════════════
CLUSTER_STATE=$(aws ecs describe-clusters --region "${AWS_REGION}" --cluster "${CLUSTER}" \
    --query 'clusters[0].status' --output text 2>/dev/null || true)
if [ "${CLUSTER_STATE}" = "ACTIVE" ]; then
    log "集群已存在: ${CLUSTER}"                          # 幂等：存在就跳过，重复部署无副作用
else
    aws ecs create-cluster --region "${AWS_REGION}" --cluster-name "${CLUSTER}" >/dev/null
    log "集群已创建: ${CLUSTER}"                          # 仅首次部署会走到这（集群=免费空壳）
fi

# ════════════════ 第 3 步：后端（PART=backend|all）═══════════════
if [ "${PART}" = "backend" ] || [ "${PART}" = "all" ]; then
    # 渲染 + 注册任务定义：AWS 侧不可修改，每次注册产生新版本号（revision），天然形成发布历史
    python "$SCRIPT_DIR_W/taskdef.py" --template "${TPL}" --image "${IMAGE}" \
        --set FAMILY="${FAMILY}" \
        --set EXECUTION_ROLE_ARN="${EXECUTION_ROLE_ARN}" \
        --set TASK_ROLE_ARN="${TASK_ROLE_ARN}" \
        --set SSM_PREFIX="${SSM_PREFIX}" \
        --set LOG_GROUP="${LOG_GROUP}" \
        --set SQS_QUEUE_URL="${SQS_URL}" \
        --set SES_CONFIGURATION_SET="${CONFIG_SET}" \
        --set UNSUBSCRIBE_BASE_URL="https://${SITE_DOMAIN}" \
        --output "${TASKDEF_JSON}" || die "后端任务定义渲染失败（检查命名资源是否建齐）"

    REVISION=$(aws ecs register-task-definition --region "${AWS_REGION}" \
        --cli-input-json "file://${TASKDEF_JSON}" \
        --query 'taskDefinition.revision' --output text)
    log "后端任务定义已注册: ${FAMILY}:${REVISION}"

    # 服务存在性判断必须精确等于 ACTIVE：INACTIVE（已删除的墓碑态）含 ACTIVE 子串，
    # 用 grep 会误命中 → 误走更新分支（实测踩过）
    SVC_STATE=$(aws ecs describe-services --region "${AWS_REGION}" --cluster "${CLUSTER}" --services "${SERVICE}" \
        --query 'services[0].status' --output text 2>/dev/null) || SVC_STATE=""
    if [ "${SVC_STATE}" = "ACTIVE" ]; then
        # ── 服务已存在：滚动更新（把服务指向新 revision，ECS 自动停旧任务、起新任务）──
        aws ecs update-service --region "${AWS_REGION}" --cluster "${CLUSTER}" --service "${SERVICE}" \
            --task-definition "${FAMILY}:${REVISION}" \
            --deployment-configuration "${DEPLOY_CFG}" >/dev/null
        log "滚动更新后端服务: ${SERVICE} → revision ${REVISION}"
        # 目标组更名守卫：服务的 loadBalancers 创建后不可改——若仍挂旧组，提示按 9C 流程重建
        REGISTERED_TG=$(aws ecs describe-services --region "${AWS_REGION}" --cluster "${CLUSTER}" \
            --services "${SERVICE}" --query 'services[0].loadBalancers[0].targetGroupArn' --output text)
        if [ "${REGISTERED_TG}" != "${TG_ARN}" ]; then
            log "⚠️ 注意: 后端服务仍挂旧目标组 ${REGISTERED_TG##*targetgroup/}"
            log "   挂新组 ${TG_NAME} 需按 docs/DEPLOY.md 服务重建流程：控制台删服务后重跑本命令（走首建分支）"
        fi
    else
        # ── 服务不存在：首次创建（挂后端目标组、配置网络与副本数）──
        aws ecs create-service --region "${AWS_REGION}" --cluster "${CLUSTER}" --service-name "${SERVICE}" \
            --task-definition "${FAMILY}:${REVISION}" \
            --desired-count 1 --launch-type FARGATE \
            --network-configuration "${NETWORK_CONFIG}" \
            --load-balancers "targetGroupArn=${TG_ARN},containerName=backend,containerPort=8000" \
            --service-connect-configuration "enabled=true,namespace=${NS_ID},services=[{portName=app,discoveryName=backend,clientAliases=[{port=80,dnsName=backend}]}]" \
            --deployment-configuration "${DEPLOY_CFG}" \
            --health-check-grace-period 60 >/dev/null
        log "首次创建后端服务: ${SERVICE}（desiredCount=1，挂 ${TG_NAME}）"
    fi
fi

# ════════════════ 第 4 步：前端（PART=frontend|all）═══════════════
if [ "${PART}" = "frontend" ] || [ "${PART}" = "all" ]; then
    # 前端模板无 SSM 密钥/无任务角色：唯一环境变量 BACKEND_URL=内网通道（VPC 内解析 ALB 私有IP）
    python "$SCRIPT_DIR_W/taskdef.py" --template "${TPL_FE}" --image "${IMAGE_FE}" \
        --set FAMILY="${FAMILY_FE}" \
        --set EXECUTION_ROLE_ARN="${EXECUTION_ROLE_ARN}" \
        --set LOG_GROUP="${LOG_GROUP_FE}" \
        --set BACKEND_URL="http://${BACKEND_DNS}" \
        --output "${TASKDEF_JSON}" || die "前端任务定义渲染失败"

    REVISION_FE=$(aws ecs register-task-definition --region "${AWS_REGION}" \
        --cli-input-json "file://${TASKDEF_JSON}" \
        --query 'taskDefinition.revision' --output text)
    log "前端任务定义已注册: ${FAMILY_FE}:${REVISION_FE}"

    SVC_STATE_FE=$(aws ecs describe-services --region "${AWS_REGION}" --cluster "${CLUSTER}" --services "${SERVICE_FE}" \
        --query 'services[0].status' --output text 2>/dev/null) || SVC_STATE_FE=""
    if [ "${SVC_STATE_FE}" = "ACTIVE" ]; then
        # ── 服务已存在：滚动更新（100/200 = 先起新、健康、才停旧 → 零停机）──
        aws ecs update-service --region "${AWS_REGION}" --cluster "${CLUSTER}" --service "${SERVICE_FE}" \
            --task-definition "${FAMILY_FE}:${REVISION_FE}" \
            --deployment-configuration "${DEPLOY_CFG_FE}" >/dev/null
        log "滚动更新前端服务: ${SERVICE_FE} → revision ${REVISION_FE}（零停机）"
    else
        # ── 服务不存在：首次创建（挂前端目标组 :3000）──
        aws ecs create-service --region "${AWS_REGION}" --cluster "${CLUSTER}" --service-name "${SERVICE_FE}" \
            --task-definition "${FAMILY_FE}:${REVISION_FE}" \
            --desired-count 1 --launch-type FARGATE \
            --network-configuration "${NETWORK_CONFIG}" \
            --load-balancers "targetGroupArn=${TG_ARN_FE},containerName=frontend,containerPort=3000" \
            --service-connect-configuration "enabled=true,namespace=${NS_ID}" \
            --deployment-configuration "${DEPLOY_CFG_FE}" \
            --health-check-grace-period 60 >/dev/null
        log "首次创建前端服务: ${SERVICE_FE}（desiredCount=1，挂 ${TG_NAME_FE}）"
    fi
fi

# ════════════════ 第 5 步：等待滚动完成 ════════════════
# services-stable 会阻塞到"期望副本=运行中副本"（首次拉镜像+建表约 2-4 分钟）
if [ "${PART}" = "backend" ] || [ "${PART}" = "all" ]; then
    log "等待后端服务稳定（首次拉镜像+建表可能 2-4 分钟）..."
    aws ecs wait services-stable --region "${AWS_REGION}" --cluster "${CLUSTER}" --services "${SERVICE}" \
        || die "后端服务未稳定——请查日志: aws logs tail ${LOG_GROUP} --region ${AWS_REGION} --follow"
fi
if [ "${PART}" = "frontend" ] || [ "${PART}" = "all" ]; then
    log "等待前端服务稳定（首次拉镜像约 2-4 分钟）..."
    aws ecs wait services-stable --region "${AWS_REGION}" --cluster "${CLUSTER}" --services "${SERVICE_FE}" \
        || die "前端服务未稳定——请查日志: aws logs tail ${LOG_GROUP_FE} --region ${AWS_REGION} --follow"
fi

# ════════════════ 完成 ════════════════
log "✅ 发布完成: ${TAG} [${PART}]"
log "   访问入口: http://${ALB_DNS}/"
log "   后端日志: aws logs tail ${LOG_GROUP} --region ${AWS_REGION} --follow"
log "   前端日志: aws logs tail ${LOG_GROUP_FE} --region ${AWS_REGION} --follow"
