package compactor

import (
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

func TestEstimateTokenAsciiAndHan(t *testing.T) {
	// 4 个 ASCII 字符 ≈ 1 token
	if got := EstimateToken("abcd"); got < 0.9 || got > 1.1 {
		t.Errorf("EstimateToken(abcd) = %v, 期望约 1", got)
	}
	// 3 个汉字 ≈ 2 token（1.5 rune/token）
	if got := EstimateToken("三个字"); got < 1.9 || got > 2.1 {
		t.Errorf("EstimateToken(三个字) = %v, 期望约 2", got)
	}
	// 空串 = 0
	if got := EstimateToken(""); got != 0 {
		t.Errorf("EstimateToken(\"\") = %v, 期望 0", got)
	}
	// 中英混排：8 个 ascii + 3 个汉字 = 2 + 2 = 4
	if got := EstimateToken("abcdefgh三个字"); got < 3.9 || got > 4.1 {
		t.Errorf("EstimateToken 混排 = %v, 期望约 4", got)
	}
}

func TestEstimateTokenIntCeil(t *testing.T) {
	if got := EstimateTokenInt(""); got != 1 {
		t.Errorf("EstimateTokenInt(\"\") = %d, 期望 1（实现恒 +1 向上取整）", got)
	}
	if got := EstimateTokenInt("abcd"); got != 2 {
		t.Errorf("EstimateTokenInt(abcd) = %d, 期望 2", got)
	}
	if got := EstimateTokenInt(strings.Repeat("a", 400)); got != 101 {
		t.Errorf("EstimateTokenInt(400 ascii) = %d, 期望 101", got)
	}
	if got := EstimateTokenInt("三个字"); got != 3 {
		t.Errorf("EstimateTokenInt(三个字) = %d, 期望 3（2+1 取整）", got)
	}
}

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
	out, res := SimpleCompactor.Compress(msgs, 200_000, win)
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
	out, res := SimpleCompactor.Compress(msgs, 200_000, win)
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
	if res.InputTokenCompressed <= 0 || res.OutputTokenCompressed <= 0 {
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
	out, res := SimpleCompactor.Compress(msgs, 1, sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 0})
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
	out, res := SimpleCompactor.Compress(msgs, 1, sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 0})
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
	_, res := SimpleCompactor.Compress(msgs, 1, sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 0})
	if res.OutputTokenCompressed <= 0 {
		t.Errorf("非 in-memory 超长 assistant 输出应计入 output 侧压缩，实际 %+v", res)
	}
}

func TestCompressResultTotal(t *testing.T) {
	res := &CompressResult{InputTokenCompressed: 30, OutputTokenCompressed: 10}
	if res.Total() != 40 {
		t.Errorf("Total() = %d, 期望 40", res.Total())
	}
}

func TestStrategyInterfaceSatisfied(t *testing.T) {
	var _ Strategy = SimpleCompactor
}
