package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// fakeCompactor 是 Compactor 端口的测试替身：记录收到的窗口占用与阈值，
// 按预设返回压缩结果，用于验证聚合把哪些状态交给策略、又如何回收结果。
type fakeCompactor struct {
	gotMaxToken int
	gotWin      sharedkernel.TokenStatistics
	gotMsgCnt   int
	retMsgs     []sharedkernel.Message
	retSaved    sharedkernel.TokenStatistics
	retErr      error
	calls       int
}

func (f *fakeCompactor) Compress(msgs []sharedkernel.Message, maxToken int,
	winConsumed sharedkernel.TokenStatistics) ([]sharedkernel.Message, sharedkernel.TokenStatistics, error) {
	f.calls++
	f.gotMsgCnt = len(msgs)
	f.gotMaxToken = maxToken
	f.gotWin = winConsumed
	if f.retErr != nil {
		return nil, sharedkernel.TokenStatistics{}, f.retErr
	}
	if f.retMsgs != nil {
		return f.retMsgs, f.retSaved, nil
	}
	return msgs, f.retSaved, nil
}

func TestNewSessionDefaultID(t *testing.T) {
	s := NewSession("")
	if s.ID == "" {
		t.Fatal("sessionID 为空时应生成默认时间戳 ID")
	}
	if s.Messages != nil {
		t.Errorf("新会话消息应为空，实际 %+v", s.Messages)
	}
	if s.TokenUsed != (sharedkernel.TokenStatistics{}) || s.WindowToken != (sharedkernel.TokenStatistics{}) {
		t.Errorf("新会话 token 账目应为零值，实际 %+v / %+v", s.TokenUsed, s.WindowToken)
	}
}

func TestNewSessionKeepsGivenID(t *testing.T) {
	s := NewSession("abc-123")
	if s.ID != "abc-123" {
		t.Errorf("应保留传入的 ID，实际 %q", s.ID)
	}
}

func TestUpsertSysMessageOnEmptySession(t *testing.T) {
	s := NewSession("s1")

	s.UpsertSysMessage("p1")

	if len(s.Messages) != 1 {
		t.Fatalf("空会话应只含 1 条系统消息，实际 %d 条", len(s.Messages))
	}
	if s.Messages[0].Role != sharedkernel.RoleSystem || s.Messages[0].Content != "p1" {
		t.Errorf("首条应为 system/p1，实际 %+v", s.Messages[0])
	}
	if s.sysToken != sharedkernel.EstimateTokenInt("p1") {
		t.Errorf("系统提示词估算占用未记账：got %d want %d", s.sysToken, sharedkernel.EstimateTokenInt("p1"))
	}
}

// 不变量：重复设置系统提示词只替换 Messages[0]，绝不追加第二条。
func TestUpsertSysMessageReplacesInPlace(t *testing.T) {
	s := NewSession("s1")
	s.UpsertSysMessage("p1")
	s.UpsertSysMessage("p2")

	if len(s.Messages) != 1 {
		t.Fatalf("替换系统提示词不应追加消息，实际 %d 条", len(s.Messages))
	}
	if s.Messages[0].Content != "p2" {
		t.Errorf("系统提示词应更新为 p2，实际 %q", s.Messages[0].Content)
	}
	if s.sysToken != sharedkernel.EstimateTokenInt("p2") {
		t.Errorf("估算占用应跟随新提示词，got %d", s.sysToken)
	}
}

// 历史首条不是系统消息时，系统提示词插入到头部（仍满足"恒居首位"）。
func TestUpsertSysMessageInsertsBeforeNonSystemHead(t *testing.T) {
	s := NewSession("s1")
	if err := s.AppendMessage(&sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "q"}); err != nil {
		t.Fatalf("append user: %v", err)
	}

	s.UpsertSysMessage("p1")

	if len(s.Messages) != 2 {
		t.Fatalf("应插入到头部，实际 %d 条", len(s.Messages))
	}
	if s.Messages[0].Role != sharedkernel.RoleSystem || s.Messages[1].Content != "q" {
		t.Errorf("顺序应为 system → user，实际 %+v", s.Messages)
	}
}

// 边界：返回值是快照，与聚合内部状态互不别名，外部改动不得影响会话。
func TestUpsertSysMessageReturnsDetachedCopy(t *testing.T) {
	s := NewSession("s1")
	s.UpsertSysMessage("p1")

	snapshot := s.UpsertSysMessage("p2")
	snapshot.Content = "被外部改坏"
	snapshot.Role = sharedkernel.RoleUser

	if s.Messages[0].Content != "p2" || s.Messages[0].Role != sharedkernel.RoleSystem {
		t.Errorf("外部修改快照不应影响聚合，实际 %+v", s.Messages[0])
	}
	// 反向亦然：聚合内部副本与快照各自独立
	if snapshot.Content == s.Messages[0].Content {
		t.Error("快照与内部状态疑似共享同一份数据")
	}
}

// 窗口占用校正：换系统提示词时扣掉旧估算、加上新估算。
func TestUpsertSysMessageAdjustsWindowToken(t *testing.T) {
	s := NewSession("s1")
	s.UpsertSysMessage("aaaa") // 估算 4/4+1 = 2
	if s.WindowToken.TokenInput != sharedkernel.EstimateTokenInt("aaaa") {
		t.Fatalf("首次设置应把估算占用计入窗口，实际 %+v", s.WindowToken)
	}

	s.UpsertSysMessage(strings.Repeat("a", 400)) // 估算 100+1 = 101
	want := sharedkernel.EstimateTokenInt(strings.Repeat("a", 400))
	if s.WindowToken.TokenInput != want {
		t.Errorf("替换后窗口占用应为新提示词估算值 %d，实际 %+v", want, s.WindowToken)
	}
	if s.WindowToken.TokenOutput != 0 {
		t.Errorf("系统提示词只影响输入侧，实际 %+v", s.WindowToken)
	}
}

func TestAppendMessageRejectsNilAndSystem(t *testing.T) {
	s := NewSession("s1")

	if err := s.AppendMessage(nil); !errors.Is(err, ErrNilMessage) {
		t.Errorf("nil 消息应返回 ErrNilMessage，实际 %v", err)
	}
	// 系统消息只能走 UpsertSysMessage：混进追加路径会让续聊读回两条系统提示词
	err := s.AppendMessage(&sharedkernel.Message{Role: sharedkernel.RoleSystem, Content: "p"})
	if !errors.Is(err, ErrSystemViaAppend) {
		t.Errorf("system 消息应返回 ErrSystemViaAppend，实际 %v", err)
	}
	if len(s.Messages) != 0 {
		t.Errorf("被拒的消息不应进入序列，实际 %d 条", len(s.Messages))
	}
}

func TestAppendMessageAssistantSettlesTokens(t *testing.T) {
	s := NewSession("s1")
	s.UpsertSysMessage("system prompt")
	if err := s.AppendMessage(&sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "hi"}); err != nil {
		t.Fatalf("append user: %v", err)
	}

	// 第一轮：实测用量整体覆盖窗口占用，累计用量增加
	if err := s.AppendMessage(&sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		Content:   "a1",
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 20},
	}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}
	if s.TokenUsed != (sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 20}) {
		t.Errorf("累计用量应等于本次实测值，实际 %+v", s.TokenUsed)
	}
	if s.WindowToken != (sharedkernel.TokenStatistics{TokenInput: 100, TokenOutput: 20}) {
		t.Errorf("窗口占用应以最近一次实测值为准，实际 %+v", s.WindowToken)
	}

	// 第二轮：累计叠加，窗口覆盖
	if err := s.AppendMessage(&sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: 150, TokenOutput: 30},
	}); err != nil {
		t.Fatalf("append assistant 2: %v", err)
	}
	if s.TokenUsed != (sharedkernel.TokenStatistics{TokenInput: 250, TokenOutput: 50}) {
		t.Errorf("累计用量应叠加为 250/50，实际 %+v", s.TokenUsed)
	}
	if s.WindowToken != (sharedkernel.TokenStatistics{TokenInput: 150, TokenOutput: 30}) {
		t.Errorf("窗口占用应覆盖为本次 150/30，实际 %+v", s.WindowToken)
	}

	// user / tool 消息不参与结算
	if err := s.AppendMessage(&sharedkernel.Message{Role: sharedkernel.RoleTool, Content: "out", ToolCallID: "c1"}); err != nil {
		t.Fatalf("append tool: %v", err)
	}
	if s.TokenUsed != (sharedkernel.TokenStatistics{TokenInput: 250, TokenOutput: 50}) {
		t.Errorf("tool 消息不应改动累计用量，实际 %+v", s.TokenUsed)
	}
	// 聚合只存副本：调用方事后改动指针不影响已入序列的消息
	msg := &sharedkernel.Message{Role: sharedkernel.RoleUser, Content: "original"}
	if err := s.AppendMessage(msg); err != nil {
		t.Fatalf("append user 2: %v", err)
	}
	msg.Content = "mutated"
	if s.Messages[len(s.Messages)-1].Content != "original" {
		t.Errorf("聚合应保存副本，实际 %q", s.Messages[len(s.Messages)-1].Content)
	}
}

func TestLoadMessagesAdoptsLeadingSystem(t *testing.T) {
	s := NewSession("s1")
	s.LoadMessages([]sharedkernel.Message{
		{Role: sharedkernel.RoleSystem, Content: strings.Repeat("a", 40)},
		{Role: sharedkernel.RoleUser, Content: "q"},
	})

	if len(s.Messages) != 2 {
		t.Fatalf("应原样认领历史，实际 %d 条", len(s.Messages))
	}
	if s.sysToken != sharedkernel.EstimateTokenInt(strings.Repeat("a", 40)) {
		t.Errorf("应从首条系统消息重算估算占用，实际 %d", s.sysToken)
	}

	// 首条不是系统消息：估算占用清零，不留悬挂值
	s.LoadMessages([]sharedkernel.Message{{Role: sharedkernel.RoleUser, Content: "q"}})
	if s.sysToken != 0 {
		t.Errorf("无系统消息时估算占用应为 0，实际 %d", s.sysToken)
	}
	s.LoadMessages(nil)
	if s.sysToken != 0 || len(s.Messages) != 0 {
		t.Errorf("加载空历史应清空状态，实际 %+v / %d", s.sysToken, len(s.Messages))
	}
}

func TestLoadAndReadMeta(t *testing.T) {
	s := NewSession("s1")
	s.LoadMeta(sharedkernel.SessionMeta{
		TokenUsed:   sharedkernel.TokenStatistics{TokenInput: 300, TokenOutput: 60},
		WindowToken: sharedkernel.TokenStatistics{TokenInput: 120, TokenOutput: 15},
	})

	if s.TokenUsed.TokenInput != 300 || s.TokenUsed.TokenOutput != 60 {
		t.Errorf("TokenUsed 应从 meta 恢复，实际 %+v", s.TokenUsed)
	}
	// WindowToken 恢复的是窗口占用，而不是旧实现里的 TokenUsed
	if s.WindowToken.TokenInput != 120 || s.WindowToken.TokenOutput != 15 {
		t.Errorf("WindowToken 应从 meta.WindowToken 恢复，实际 %+v", s.WindowToken)
	}

	// 账目快照供 application 层落盘
	s.AppendMessage(&sharedkernel.Message{
		Role:      sharedkernel.RoleAssistant,
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: 10, TokenOutput: 2},
	})
	meta := s.Meta()
	if meta.TokenUsed != (sharedkernel.TokenStatistics{TokenInput: 310, TokenOutput: 62}) {
		t.Errorf("Meta() 应反映最新累计用量，实际 %+v", meta)
	}
	if meta.WindowToken != (sharedkernel.TokenStatistics{TokenInput: 10, TokenOutput: 2}) {
		t.Errorf("Meta() 应反映最新窗口占用，实际 %+v", meta)
	}
}

// 压缩触发判据用窗口占用（而非只增不减的累计用量），节省量从窗口扣除。
func TestCompactUsesWindowTokenAndDeductsSaved(t *testing.T) {
	s := NewSession("s1")
	s.UpsertSysMessage("system prompt")
	s.LoadMeta(sharedkernel.SessionMeta{
		TokenUsed:   sharedkernel.TokenStatistics{TokenInput: 900_000, TokenOutput: 100},
		WindowToken: sharedkernel.TokenStatistics{TokenInput: 180_000, TokenOutput: 2_000},
	})
	fake := &fakeCompactor{retSaved: sharedkernel.TokenStatistics{TokenInput: 3_000, TokenOutput: 500}}

	if err := s.Compact(fake, 200_000); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("策略应被调用一次，实际 %d", fake.calls)
	}
	if fake.gotMaxToken != 200_000 {
		t.Errorf("阈值应原样传给策略，实际 %d", fake.gotMaxToken)
	}
	if fake.gotWin != (sharedkernel.TokenStatistics{TokenInput: 180_000, TokenOutput: 2_000}) {
		t.Errorf("触发判据应为窗口占用而非累计用量，实际传给策略的是 %+v", fake.gotWin)
	}
	want := sharedkernel.TokenStatistics{TokenInput: 177_000, TokenOutput: 1_500}
	if s.WindowToken != want {
		t.Errorf("窗口占用应扣减节省量 %+v，实际 %+v", want, s.WindowToken)
	}
	// 累计用量是计费口径，压缩不得改动
	if s.TokenUsed != (sharedkernel.TokenStatistics{TokenInput: 900_000, TokenOutput: 100}) {
		t.Errorf("压缩不应改动累计用量，实际 %+v", s.TokenUsed)
	}
}

func TestCompactAdoptsCompressedMessages(t *testing.T) {
	s := NewSession("s1")
	s.UpsertSysMessage("system prompt")
	compressed := []sharedkernel.Message{
		{Role: sharedkernel.RoleSystem, Content: "system prompt"},
		{Role: sharedkernel.RoleUser, Content: "已被系统清理"},
	}
	fake := &fakeCompactor{retMsgs: compressed}

	if err := s.Compact(fake, 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(s.Messages) != 2 || s.Messages[1].Content != "已被系统清理" {
		t.Fatalf("压缩结果应回写序列，实际 %+v", s.Messages)
	}
	if s.sysToken != sharedkernel.EstimateTokenInt("system prompt") {
		t.Errorf("回写后应重新认领系统提示词占用，实际 %d", s.sysToken)
	}
}

func TestCompactNilStrategyAndError(t *testing.T) {
	s := NewSession("s1")
	if err := s.Compact(nil, 100); !errors.Is(err, ErrNilCompactor) {
		t.Errorf("nil 策略应返回 ErrNilCompactor，实际 %v", err)
	}

	boom := errors.New("compress failed")
	s2 := NewSession("s2")
	s2.LoadMessages([]sharedkernel.Message{{Role: sharedkernel.RoleUser, Content: "q"}})
	if err := s2.Compact(&fakeCompactor{retErr: boom}, 100); !errors.Is(err, boom) {
		t.Errorf("策略错误应透传，实际 %v", err)
	}
	// 失败时不得改动聚合状态
	if len(s2.Messages) != 1 || s2.WindowToken != (sharedkernel.TokenStatistics{}) {
		t.Errorf("压缩失败不应改动状态，实际 %+v / %+v", s2.Messages, s2.WindowToken)
	}
}
