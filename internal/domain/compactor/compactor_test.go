package compactor

import (
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// compressMsgs 生成一段典型的 ReAct 历史：user 提问 → 工具输出 → assistant 回答。
func compressMsgs() []sharedkernel.Message {
	return []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "用户提问：请帮我统计一下"},
		{Role: sharedkernel.RoleUser, ToolCallID: "call-1", Content: strings.Repeat("工具大输出", 500)},
		{Role: sharedkernel.RoleAssistant, Content: "assistant 回答", ReasoningContent: strings.Repeat("思考过程", 200)},
		{Role: sharedkernel.RoleUser, ToolCallID: "call-2", Content: strings.Repeat("工具输出2", 300)},
		{Role: sharedkernel.RoleAssistant, Content: "final", ReasoningContent: "最新推理"},
	}
}

func TestCompressBelowThresholdNoop(t *testing.T) {
	msgs := compressMsgs()
	win := sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 50}
	out, res, err := SimpleCompactor.Compress(msgs, 200_000, win)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if res.Total() != 0 {
		t.Errorf("未达阈值不应压缩，实际节省 %d", res.Total())
	}
	// 原地返回同一切片
	if len(out) != len(msgs) {
		t.Errorf("消息数量应不变，实际 %d", len(out))
	}
	for i := range msgs {
		if out[i].Content != msgs[i].Content {
			t.Errorf("未达阈值不应改动内容：第 %d 条", i)
		}
	}
}

// 阈值判据读的是窗口占用总量（Total），故输出侧占用同样能触发压缩。
func TestCompressThresholdCountsBothSides(t *testing.T) {
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		{Role: sharedkernel.RoleUser, ToolCallID: "c1", Content: strings.Repeat("工具输出", 500)},
	}
	// 输入侧 0、输出侧 200：合计达到 maxToken=100 的 80% 以上 → 应压缩
	_, res, err := SimpleCompactor.Compress(msgs, 100, sharedkernel.TokenStatistics{TokenOutput: 200})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if res.Total() <= 0 {
		t.Errorf("输出侧占用也应触发压缩，实际 %+v", res)
	}
}

func TestCompressAboveThresholdCleansOldToolOutput(t *testing.T) {
	// 历史形如：q1 → 工具输出1 → 早期回答 → q2(最后人类输入) → 工具输出2 → final
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q1"},
		{Role: sharedkernel.RoleUser, ToolCallID: "call-1", Content: strings.Repeat("工具输出1", 500)},
		{Role: sharedkernel.RoleAssistant, Content: "assistant 早期回答", ReasoningContent: strings.Repeat("早期思考", 200)},
		{Role: sharedkernel.RoleUser, Content: "q2"},
		{Role: sharedkernel.RoleUser, ToolCallID: "call-2", Content: strings.Repeat("工具输出2", 300)},
		{Role: sharedkernel.RoleAssistant, Content: "final", ReasoningContent: "最新推理"},
	}
	win := sharedkernel.TokenStatistics{TokenInput: 200_000, TokenOutput: 0}
	out, res, err := SimpleCompactor.Compress(msgs, 200_000, win)
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if res.Total() <= 0 {
		t.Fatal("超过阈值应产生压缩收益")
	}
	// 早于最后一条（in-memory）的工具输出被替换为清理说明
	for _, idx := range []int{1, 4} {
		if !strings.Contains(out[idx].Content, "已被系统清理") {
			t.Errorf("第 %d 条（非 in-memory 工具输出）应被清理说明替换，实际：%q", idx+1, out[idx].Content[:60])
		}
	}
	// 最后一条消息完整保留
	if out[len(out)-1].Content != "final" {
		t.Errorf("最后一条消息应完整保留，实际：%q", out[len(out)-1].Content)
	}
	// 早于最后一次人类输入的旧 reasoning 被丢弃
	if out[2].ReasoningContent != "" {
		t.Errorf("陈旧 reasoning 应被清空，实际：%q", out[2].ReasoningContent)
	}
	if out[len(out)-1].ReasoningContent != "最新推理" {
		t.Errorf("最后一次人类输入之后的 reasoning 应保留，实际：%q", out[len(out)-1].ReasoningContent)
	}
	// 输入输出两侧都应记录压缩收益
	if res.TokenInput <= 0 || res.TokenOutput <= 0 {
		t.Errorf("输入输出两侧都应记录压缩收益：%+v", res)
	}
}

func TestCompressInMemoryToolOutputTruncatesOnlyLong(t *testing.T) {
	// 只有 2 条消息：最后一条（in-memory）为超长工具输出 → 应截断保留头尾
	long := strings.Repeat("x", 3000)
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		{Role: sharedkernel.RoleUser, ToolCallID: "c1", Content: long},
	}
	out, res, err := SimpleCompactor.Compress(msgs, 1, sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 0})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if res.Total() <= 0 {
		t.Fatal("长工具输出应触发截断")
	}
	if !strings.Contains(out[1].Content, "输出过长") {
		t.Errorf("应截断并标注，实际开头：%q", out[1].Content[:100])
	}
	if !strings.HasSuffix(out[1].Content, strings.Repeat("x", 500)) {
		t.Error("截断应保留末尾 500 字节")
	}
}

func TestCompressShortInMemoryToolOutputKept(t *testing.T) {
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		{Role: sharedkernel.RoleUser, ToolCallID: "c1", Content: "short tool output"},
	}
	out, res, err := SimpleCompactor.Compress(msgs, 1, sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 0})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if res.Total() != 0 {
		t.Errorf("in-memory 短工具输出不应压缩，实际 %+v", res)
	}
	if out[1].Content != "short tool output" {
		t.Errorf("内容不应被改动，实际 %q", out[1].Content)
	}
}

func TestCompressLongAssistantOutputTruncated(t *testing.T) {
	long := strings.Repeat("a", 2000)
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleUser, Content: "q"},
		{Role: sharedkernel.RoleAssistant, Content: long},
		{Role: sharedkernel.RoleUser, Content: "next"},
	}
	_, res, err := SimpleCompactor.Compress(msgs, 1, sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 0})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if res.TokenOutput <= 0 {
		t.Errorf("非 in-memory 超长 assistant 输出应计入 output 侧压缩，实际 %+v", res)
	}
}

// 压缩不得改动系统消息：它是每轮启动重写的人格提示词，不参与裁剪。
func TestCompressKeepsSystemMessage(t *testing.T) {
	sys := "系统提示词" + strings.Repeat("长", 2000)
	msgs := []sharedkernel.Message{
		{Role: sharedkernel.RoleSystem, Content: sys},
		{Role: sharedkernel.RoleUser, Content: "q"},
		{Role: sharedkernel.RoleUser, ToolCallID: "c1", Content: strings.Repeat("工具输出", 500)},
	}
	out, _, err := SimpleCompactor.Compress(msgs, 1, sharedkernel.TokenStatistics{TokenInput: 100})
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if out[0].Role != sharedkernel.RoleSystem || out[0].Content != sys {
		t.Errorf("系统消息应原样保留，实际 %q", out[0].Content[:40])
	}
}

func TestStrategyInterfaceSatisfied(t *testing.T) {
	var _ Strategy = SimpleCompactor
}
