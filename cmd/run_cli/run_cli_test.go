package run_cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/application/reactservice"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestEventConsumerStreamsTextAndReasoning(t *testing.T) {
	var output []string
	rcf := newEventConsumer(func(s string) { output = append(output, s) })
	chunks := []sharedkernel.StreamChunk{
		{Kind: sharedkernel.ChunkReasoningStart},
		{Kind: sharedkernel.ChunkReasoningDelta, Delta: "先思考"},
		{Kind: sharedkernel.ChunkReasoningDelta, Delta: "再回答"},
		{Kind: sharedkernel.ChunkReasoningEnd},
		{Kind: sharedkernel.ChunkTextStart},
		{Kind: sharedkernel.ChunkTextDelta, Delta: "hello"},
		{Kind: sharedkernel.ChunkTextDelta, Delta: " world"},
		{Kind: sharedkernel.ChunkTextEnd},
	}
	want := []string{
		ColorGray + "[LaxCode] thinking: ", ColorGray + "先思考" + ColorReset, ColorGray + "再回答" + ColorReset, ColorReset + "\n",
		ColorGreen + "[LaxCode] LLM generates: ", ColorGreen + "hello" + ColorReset, ColorGreen + " world" + ColorReset, ColorReset + "\n",
	}
	for i, chunk := range chunks {
		rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeChunk, ChunkEvent: &chunk})
		if !reflect.DeepEqual(output, want[:i+1]) {
			t.Fatalf("第 %d 个 chunk 应立即输出且不重复前缀：got %q, want %q", i, output, want[:i+1])
		}
	}
}

func TestEventConsumerShowsToolOnlyAtExecution(t *testing.T) {
	var output []string
	rcf := newEventConsumer(func(s string) { output = append(output, s) })
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeChunk})
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeChunk, ChunkEvent: &sharedkernel.StreamChunk{
		Kind: sharedkernel.ChunkToolCall, ToolCall: &sharedkernel.ToolCall{Name: "bash"},
	}})
	if len(output) != 0 {
		t.Fatalf("参数就绪时不应提前显示工具执行提示：%q", output)
	}
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeToolCall, Content: "bash: ls"})
	want := []string{ColorYellow + "[LaxCode] tool execute... bash: ls" + ColorReset + "\n"}
	if !reflect.DeepEqual(output, want) {
		t.Fatalf("工具执行提示不符：got %q, want %q", output, want)
	}
}

func TestEventConsumerShowsRecovery(t *testing.T) {
	var output []string
	rcf := newEventConsumer(func(s string) { output = append(output, s) })
	rcf(&reactservice.ReactEvent{Type: reactservice.ReActEventTypeRecovery, Content: "正在恢复"})
	want := []string{ColorYellow + "[LaxCode] 正在恢复" + ColorReset + "\n"}
	if !reflect.DeepEqual(output, want) {
		t.Fatalf("恢复提示不符：got %q, want %q", output, want)
	}
}

func TestFormatRuntimeErrorAddsPersistRetryHint(t *testing.T) {
	got := formatRuntimeError(errors.Join(reactservice.ErrPersistRequestContext, errors.New("disk full")))
	if !strings.Contains(got, "下次对话开始前先恢复上一轮") {
		t.Fatalf("持久化错误应提示下次输入自动恢复：%q", got)
	}
	plain := formatRuntimeError(errors.New("provider error"))
	if strings.Contains(plain, "下次对话开始前先恢复上一轮") {
		t.Fatalf("普通错误不应显示持久化恢复提示：%q", plain)
	}
}
