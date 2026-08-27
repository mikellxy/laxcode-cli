package engine

import (
	"context"
	"time"

	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
)

// MonitoredProvider 是包装真实 LLM provider 的监控装饰器：在 Generate
// 前后记录耗时，调用成功后把本轮 token 用量与耗时上报给构造时绑定的
// session 统一记账（session.Append 不再从消息提取用量）。观测不改变
// 调用行为，消息与 Info 均原样透传 inner。
type MonitoredProvider struct {
	inner provider.Provider
	sess  *Session
}

// NewMonitoredProvider 以 session 构造监控装饰器：调用方须先装配会话
// （main 用 GetSession、SubAgent 先建子会话），再包装 provider。
func NewMonitoredProvider(inner provider.Provider, sess *Session) *MonitoredProvider {
	return &MonitoredProvider{inner: inner, sess: sess}
}

func (m *MonitoredProvider) Info() *provider.Info {
	return m.inner.Info()
}

// Generate 调用 inner 并计时。仅成功调用产生观测记录（失败无消息产生，
// 不记账，与"调用失败不记录用量"的口径一致）；耗时取整到毫秒，
// token 用量取自返回消息的 TokenUsed 字段。
func (m *MonitoredProvider) Generate(ctx context.Context, msgs []schema.Message, toolsDefs []schema.ToolDefinition) (*schema.Message, error) {
	start := time.Now()
	msg, err := m.inner.Generate(ctx, msgs, toolsDefs)
	if err != nil {
		return nil, err
	}
	m.sess.RecordGenerate(RoundStat{
		TimeUsed:    time.Since(start).Milliseconds(),
		TokenInput:  msg.TokenUsed.TokenInput,
		TokenOutput: msg.TokenUsed.TokenOutput,
	})
	return msg, nil
}
