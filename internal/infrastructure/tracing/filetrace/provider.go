// Package filetrace 提供一个把 span 以 JSON Lines 形式追加到本地文件的
// trace.TracerProvider 实现。它只依赖 OTel API，不引入 SDK，用于在
// 没有外部 collector 时做本地 trace 落盘。
package filetrace

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// Provider 是文件日志型的 TracerProvider：每个 span 在 End 时序列化为
// 一行 JSON 并追加到指定文件。
type Provider struct {
	embedded.TracerProvider

	mu   sync.Mutex
	file *os.File
	path string
}

// New 创建一个把 trace 日志追加到 path 的 Provider。它会自动创建所在目录。
// path 通常取 ${work_dir}/.laxcode/.session/log/tracing.log。
func New(path string) (*Provider, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create trace log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open trace log file: %w", err)
	}
	return &Provider{file: f, path: path}, nil
}

// Tracer 返回属于本 Provider 的 Tracer。scope 名与选项目前仅用于合规，
// 不写入日志（调用方 instrumentation name 会作为 span 属性出现）。
func (p *Provider) Tracer(name string, _ ...trace.TracerOption) trace.Tracer {
	return &tracer{provider: p, scopeName: name}
}

// Shutdown 关闭日志文件；tracing.Handle 在进程退出前会调用它。
func (p *Provider) Shutdown(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file == nil {
		return nil
	}
	err := p.file.Close()
	p.file = nil
	return err
}

// appendLine 把一条记录序列化为 JSON 并追加到文件。
func (p *Provider) appendLine(record *logRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal trace record: %w", err)
	}
	data = append(data, '\n')

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.file == nil {
		return fmt.Errorf("trace log file already closed")
	}
	if _, err := p.file.Write(data); err != nil {
		return fmt.Errorf("write trace log: %w", err)
	}
	return nil
}
