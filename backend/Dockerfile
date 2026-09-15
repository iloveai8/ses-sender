# 多阶段构建：构建期用 alpine 工具链，运行期 debian-slim
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/ses-sender ./cmd/ses-sender

# 运行镜像：WORKDIR=/app 与 Python 版一致（存量附件的绝对路径兼容，见 02-ARCHITECTURE §5）
FROM debian:bookworm-slim
WORKDIR /app
COPY --from=builder /out/ses-sender /app/ses-sender
# 默认配置烤进镜像（yaml 即默认配置）：无挂载无 env 时开箱即用；
# compose 挂载 ./backend/config 会覆盖它；生产密钥经 env 注入（env > yaml）
COPY config/config.example.yaml /app/config/config.yaml
EXPOSE 8000
# 健康检查由 taskdef healthCheck 提供（ses-sender healthcheck 子命令），不在镜像层重复定义
ENTRYPOINT ["/app/ses-sender"]
CMD ["serve", "--mode=all"]
