# nodepanel 便捷命令
GO      ?= go
DIST    ?= dist
LDFLAGS ?= -s -w

.PHONY: help build all run agent test vet fmt clean

help:
	@echo "make build   编译本机面板端与节点端到 dist/"
	@echo "make all     交叉编译所有平台 (scripts/build.sh)"
	@echo "make run     本机启动面板（数据目录 ./.dev-panel）"
	@echo "make agent   本机启动节点端（测试用，端口 8899）"
	@echo "make test    运行测试"
	@echo "make vet     静态检查"
	@echo "make fmt     格式化代码"

build:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/nodemgr-panel ./cmd/panel
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/nodemgr-agent ./cmd/agent
	@ls -lh $(DIST)

all:
	./scripts/build.sh

run: build
	./$(DIST)/nodemgr-panel -home ./.dev-panel

agent: build
	./$(DIST)/nodemgr-agent -port 8899 -history 300

test:
	$(GO) test ./... -count=1

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(DIST) .dev-panel
