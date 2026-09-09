package run_cli

import (
	"reflect"
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
		ColorGray + "[LaxCode] thinking: ", "先思考", "再回答", ColorReset + "\n",
		ColorGreen + "[LaxCode] LLM generates: ", "hello", " world", ColorReset + "\n",
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
