package main

import (
	"log/slog"
	"os"
	"path/filepath"
)

const (
	appLogPath       = "./log/laxcode.log"
	llmRouterLogPath = "./log/llmrouter.log"
)

// configureSlog 使用标准库 JSON handler，把 INFO 及以上日志追加到文件。
// 日志目录只在进程启动时创建，不引入额外日志框架或领域端口。
func configureSlog(path string) (*os.File, error) {
	logger, f, err := newFileLogger(path)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(logger)
	return f, nil
}

// newFileLogger 创建一个不修改 slog.Default 的独立 JSON logger，供模型路由器
// 单独写入 llmrouter.log，避免和应用日志混在同一文件。
func newFileLogger(path string) (*slog.Logger, *os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	logger := slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	return logger, f, nil
}
