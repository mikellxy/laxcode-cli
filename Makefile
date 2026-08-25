# Makefile for laxcode
# 将 Go 项目二进制编译到 bin/ 目录

BINARY   := laxcode
BUILD_DIR := bin
MAIN_PKG := ./cmd/main

.PHONY: all build vet test clean

all: build

# 编译主程序到 bin/laxcode
build:
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY) $(MAIN_PKG)
	@echo "built: $(BUILD_DIR)/$(BINARY)"

# 静态检查
vet:
	go vet ./...

# 单元测试
test:
	go test ./...

# 清理编译产物
clean:
	rm -rf $(BUILD_DIR)
	@echo "cleaned: $(BUILD_DIR)/"
