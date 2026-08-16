.PHONY: build generate test test-unit test-integration test-race test-e2e

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

generate:
	go generate ./ent
	go generate ./cmd/server

test:
	go test ./...
	golangci-lint run ./...

test-unit:
	go test -tags=unit ./...

test-integration:
	go test -tags=integration ./...

# 带 -race 的两轮。必须显式带 build tag：不带 tag 时 go test -race ./internal/teamops/...
# 会输出 [no test files] 并**绿着退出** —— 那是「一个测试都没跑」，不是「跑过且没竞争」。
# 本仓的数据竞争（gin.SetMode 在 t.Parallel() 用例里）正是因此连过六轮评审。
test-race:
	go test -tags=unit -race ./...
	go test -tags=integration -race ./...

test-e2e:
	./scripts/e2e-test.sh

test-e2e-local:
	go test -tags=e2e -v -timeout=300s ./internal/integration/...
