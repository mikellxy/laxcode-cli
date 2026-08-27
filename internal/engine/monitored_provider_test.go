package engine

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/mikellxy/laxcode/internal/provider"
	"github.com/mikellxy/laxcode/internal/schema"
)

// stubProvider 是不发真实请求的 Provider 桩：返回固定消息与错误，
// 用于验证 MonitoredProvider 的观测与透传行为。
type stubProvider struct {
	msg *schema.Message
	err error
}

func (s *stubProvider) Generate(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
	return s.msg, s.err
}

func (s *stubProvider) Info() *provider.Info { return &provider.Info{Name: "stub"} }

func TestMonitoredProvider_GenerateReportsRound(t *testing.T) {
	t.Parallel()
	sess := newSession(t.TempDir(), "monitored")
	mp := NewMonitoredProvider(&stubProvider{msg: &schema.Message{
		Role:      schema.RoleAssistant,
		Content:   "答",
		TokenUsed: schema.TokenStatistics{TokenInput: 70, TokenOutput: 5},
	}}, sess)

	msg, err := mp.Generate(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 观测不改变调用行为：消息原样透传（含用量字段，供 history 重放恢复）
	if msg.TokenUsed != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("消息应原样透传: %+v", msg.TokenUsed)
	}
	if len(sess.Rounds) != 1 {
		t.Fatalf("成功调用应上报一轮观测: %+v", sess.Rounds)
	}
	round := sess.Rounds[0]
	if round.TokenInput != 70 || round.TokenOutput != 5 {
		t.Fatalf("本轮用量应取自返回消息: %+v", round)
	}
	if round.TimeUsed < 0 {
		t.Fatalf("耗时不应为负: %dms", round.TimeUsed)
	}
	if sess.TokenUsed != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("上报应累加累计消耗: %+v", sess.TokenUsed)
	}
	if sess.WindowToken != (schema.TokenStatistics{TokenInput: 70, TokenOutput: 5}) {
		t.Fatalf("上报应刷新窗口占用: %+v", sess.WindowToken)
	}
	if _, err := os.Stat(sess.metaPath); err != nil {
		t.Fatalf("上报应重写 meta.json 快照: %v", err)
	}

	// 第二轮成功后列表追加、累计消耗加和
	if _, err := mp.Generate(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(sess.Rounds) != 2 {
		t.Fatalf("每轮 generate 都应追加观测: %+v", sess.Rounds)
	}
	if sess.TokenUsed != (schema.TokenStatistics{TokenInput: 140, TokenOutput: 10}) {
		t.Fatalf("累计消耗应为两轮加和: %+v", sess.TokenUsed)
	}
}

func TestMonitoredProvider_GenerateErrorRecordsNothing(t *testing.T) {
	t.Parallel()
	sess := newSession(t.TempDir(), "monitored-err")
	stub := &stubProvider{err: errors.New("boom")}
	mp := NewMonitoredProvider(stub, sess)

	if _, err := mp.Generate(context.Background(), nil, nil); !errors.Is(err, stub.err) {
		t.Fatalf("应透传 inner 错误: %v", err)
	}
	if len(sess.Rounds) != 0 || sess.TokenUsed != (schema.TokenStatistics{}) {
		t.Fatalf("失败调用不应产生观测记录: rounds=%+v token=%+v", sess.Rounds, sess.TokenUsed)
	}
	if _, err := os.Stat(sess.metaPath); !os.IsNotExist(err) {
		t.Fatalf("失败调用不应写 meta.json")
	}
}

func TestMonitoredProvider_InfoPassthrough(t *testing.T) {
	t.Parallel()
	mp := NewMonitoredProvider(&stubProvider{}, newSession(t.TempDir(), "info"))
	if mp.Info().Name != "stub" {
		t.Fatalf("Info 应透传 inner: %+v", mp.Info())
	}
}
