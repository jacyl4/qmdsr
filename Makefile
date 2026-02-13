APP := qmdsr
VERSION := 1.0.0

PKG := qmdsr/internal/version
PROTO_FILES := proto/qmdsr/v1/query.proto proto/qmdsr/v1/admin.proto
PROTO_MODULE := qmdsr
PROTOC_GEN_GO_VERSION := v1.36.11
PROTOC_GEN_GO_GRPC_VERSION := v1.6.1

COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE := $(shell TZ=Asia/Shanghai date '+%Y-%m-%dT%H:%M:%S%:z')

LDFLAGS := -X '$(PKG).Version=$(VERSION)' \
           -X '$(PKG).Commit=$(COMMIT)' \
           -X '$(PKG).BuildTime=$(DATE)'

build:
	go build -ldflags "$(LDFLAGS)" -o $(APP) .

run:
	go run .

version:
	@echo $(VERSION)

proto-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)

proto: proto-tools
	@set -eu; \
	WKT_INCLUDE=""; \
	for inc in /usr/local/include /usr/include; do \
		if [ -f "$$inc/google/protobuf/empty.proto" ]; then \
			WKT_INCLUDE="$$inc"; \
			break; \
		fi; \
	done; \
	if [ -z "$$WKT_INCLUDE" ]; then \
		WKT_INCLUDE="$$(find "$$(go env GOMODCACHE)" -type f -path '*/google/protobuf/empty.proto' 2>/dev/null | head -n 1 | sed 's|/google/protobuf/empty.proto$$||')"; \
	fi; \
	if [ -z "$$WKT_INCLUDE" ]; then \
		echo "google/protobuf/empty.proto not found; install protobuf includes (e.g. /usr/include) or ensure module cache has well-known imports."; \
		exit 1; \
	fi; \
	PATH="$$(go env GOPATH)/bin:$$PATH" \
	protoc -I proto -I "$$WKT_INCLUDE" \
		--go_out=. --go_opt=module=$(PROTO_MODULE) \
		--go-grpc_out=. --go-grpc_opt=module=$(PROTO_MODULE) \
		$(PROTO_FILES)

.PHONY: build run version proto-tools proto
