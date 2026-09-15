# ses-sender 后端构建入口（与根目录 deploy/Makefile 的发布职责互补：本文件只管开发循环）
MODULE := ses-sender

.PHONY: build test vet lint tidy run clean

build:
	go build -o bin/ses-sender ./cmd/ses-sender

test:
	go test ./... -race -count=1

vet:
	go vet ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

run:
	go run ./cmd/ses-sender serve --mode=all

clean:
	rm -rf bin
