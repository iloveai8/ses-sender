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
