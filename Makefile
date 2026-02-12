APP := qmdsr
VERSION := 1.0.0

PKG := qmdsr/internal/version

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

.PHONY: build run version
