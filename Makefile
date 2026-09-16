GO := go
BINARY := bin/autobro
SLREGISTER_BINARY := bin/slregister
UUMAILREGISTER_BINARY := bin/uumailregister
ENV_FILE ?= .env.local
GO_FILES := $(shell find . -type f -name '*.go' -not -path './.git/*')
SOURCE_DIRS := $(sort . $(shell find cmd internal -type d))

.DEFAULT_GOAL := help

.PHONY: help build relog new newsl newslv regsl regslv reguu reguuv newuu

help: ## 显示帮助信息
	@awk 'BEGIN {FS = ":.*##"; printf "用法:\n  make <目标>\n  make <目标> ARGS=\"...\"\n\n目标:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-10s %s\n", $$1, $$2} END {printf "\n示例:\n  make new ARGS=\"-c 1 -m sl\"\n  make newslv\n  make regslv\n  make reguu\n  make newuu ARGS=\"-c 2\"\n"}' $(MAKEFILE_LIST)

build: $(BINARY) $(SLREGISTER_BINARY) $(UUMAILREGISTER_BINARY) ## 构建程序

$(BINARY): $(GO_FILES) $(SOURCE_DIRS) go.mod go.sum Makefile
	mkdir -p "$(dir $(BINARY))"
	$(GO) build -o "$(BINARY)" ./cmd/toapi

$(SLREGISTER_BINARY): $(GO_FILES) $(SOURCE_DIRS) go.mod go.sum Makefile
	mkdir -p "$(dir $(SLREGISTER_BINARY))"
	$(GO) build -o "$(SLREGISTER_BINARY)" ./cmd/slregister

$(UUMAILREGISTER_BINARY): $(GO_FILES) $(SOURCE_DIRS) go.mod go.sum Makefile
	mkdir -p "$(dir $(UUMAILREGISTER_BINARY))"
	$(GO) build -o "$(UUMAILREGISTER_BINARY)" ./cmd/uumailregister

relog: $(BINARY) ## 更新需要重新登录的账号
	set -e; set -a; . "$(ENV_FILE)"; set +a; "./$(BINARY)" -r

new: $(BINARY) ## 创建任务，可通过 ARGS 传递参数
	set -e; set -a; . "$(ENV_FILE)"; set +a; "./$(BINARY)" $(ARGS)

newsl: $(BINARY) ## 使用 SimpleLogin 创建 4 个新任务
	set -e; set -a; . "$(ENV_FILE)"; set +a; "./$(BINARY)" -m sl -c 4

newslv: $(BINARY) ## 使用 SimpleLogin 创建 4 个新任务并输出详细日志
	set -e; set -a; . "$(ENV_FILE)"; set +a; "./$(BINARY)" -v -m sl -c 4

regsl: $(SLREGISTER_BINARY) ## 注册 SimpleLogin，可通过 ARGS 传递参数
	set -e; set -a; . "$(ENV_FILE)"; set +a; "./$(SLREGISTER_BINARY)" -env "$(ENV_FILE)" $(ARGS)

regslv: $(SLREGISTER_BINARY) ## 注册 SimpleLogin并输出详细日志
	set -e; set -a; . "$(ENV_FILE)"; set +a; "./$(SLREGISTER_BINARY)" -v -env "$(ENV_FILE)"

reguu: $(UUMAILREGISTER_BINARY) ## 注册 Uumail 账号并更新 env，可通过 ARGS 传递参数
	set -e; set -a; . "$(ENV_FILE)"; set +a; "./$(UUMAILREGISTER_BINARY)" -env "$(ENV_FILE)" $(ARGS)

reguuv: $(UUMAILREGISTER_BINARY) ## 注册 Uumail 账号并输出详细日志
	set -e; set -a; . "$(ENV_FILE)"; set +a; "./$(UUMAILREGISTER_BINARY)" -v -env "$(ENV_FILE)"

newuu: $(BINARY) ## 使用 Uumail 创建 4 个新任务，ARGS 可覆盖参数
	set -e; set -a; . "$(ENV_FILE)"; set +a; "./$(BINARY)" -m uu -c 4 $(ARGS)
