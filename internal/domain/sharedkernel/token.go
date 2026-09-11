package sharedkernel

import (
	"fmt"
	"sync"
	"unicode"

	tiktoken "github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

// TokenStatistics 是一对 token 计数（输入侧 / 输出侧）。它同时承载两种口径，
// 由持有方说明语义：assistant 消息的 TokenUsed 是模型返回的实测计费口径，
// Session 的 WindowToken 是上下文窗口占用（实测值经本地估算增减校正）。
type TokenStatistics struct {
	TokenInput  int `json:"token_input"`
	TokenOutput int `json:"token_output"`
}

// Add 累加另一份统计（会话累计用量、窗口占用的增量）。
func (s *TokenStatistics) Add(other TokenStatistics) {
	s.TokenInput += other.TokenInput
	s.TokenOutput += other.TokenOutput
}

// Minus 扣减另一份统计（上下文压缩节省量、被替换的系统提示词占用）。
func (s *TokenStatistics) Minus(other TokenStatistics) {
	s.TokenInput -= other.TokenInput
	s.TokenOutput -= other.TokenOutput
}

// OverWrite 用另一份统计整体覆盖（窗口占用以最近一次实测用量为准）。
func (s *TokenStatistics) OverWrite(other TokenStatistics) {
	s.TokenInput = other.TokenInput
	s.TokenOutput = other.TokenOutput
}

// Total 返回输入侧与输出侧之和。
func (s *TokenStatistics) Total() int {
	return s.TokenInput + s.TokenOutput
}

// EstimateToken 粗估算token
// 英文(ASCII字母数字符号): 4 char = 1 token
// 中文汉字: 1.5 rune = 1 token
//
// 估算值只用于窗口预算判断（系统提示词占用、压缩节省量），不是计费口径，
// 因此不写进 Message.TokenUsed。放在 sharedkernel 是为了让 session 与
// compactor 共用同一份估算规则，避免同一算法散落两处各自漂移。
func EstimateToken(s string) float64 {
	var asciiCount int
	var cnCount int

	for _, r := range s {
		// 汉字范围
		if unicode.Is(unicode.Han, r) {
			cnCount++
		} else {
			// 英文、数字、空格、标点统一算作ascii字符
			asciiCount++
		}
	}

	tokens := float64(asciiCount)/4.0 + float64(cnCount)/1.5
	return tokens
}

// estimateEncodingName 与 llmprovider 的本地兜底计数保持一致，统一使用
// cl100k_base：它是 OpenAI 系模型的通用 BPE，对多数兼容端点都能给出量级正确的
// 估算。编码对象全局惰性加载一次后复用；离线 loader 内嵌 BPE，运行期不再联网。
const estimateEncodingName = "cl100k_base"

var (
	estimateEncodingOnce sync.Once
	estimateEncoding     *tiktoken.Tiktoken
	estimateEncodingErr  error
)

// getEstimateEncoding 惰性装配全局 tiktoken 编码，并发安全且只初始化一次。
func getEstimateEncoding() (*tiktoken.Tiktoken, error) {
	estimateEncodingOnce.Do(func() {
		tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
		estimateEncoding, estimateEncodingErr = tiktoken.GetEncoding(estimateEncodingName)
	})
	if estimateEncodingErr != nil {
		return nil, fmt.Errorf("load tiktoken encoding %q: %w", estimateEncodingName, estimateEncodingErr)
	}
	return estimateEncoding, nil
}

// EstimateTokenInt 用 tiktoken(cl100k_base) 对单段文本计数，口径与 llmprovider
// 本地兜底一致，供窗口预算判断（系统提示词占用、压缩节省量）使用；它不是计费
// 口径，因此不写进 Message.TokenUsed。放在 sharedkernel 是为了让 session 与
// compactor 共用同一份计数规则，避免同一算法散落两处各自漂移。tiktoken 万一
// 不可用则退回 EstimateToken 启发式，保证函数恒有确定返回值、不 panic。
func EstimateTokenInt(s string) int {
	encoding, err := getEstimateEncoding()
	if err != nil {
		return int(EstimateToken(s)) + 1
	}
	return len(encoding.Encode(s, nil, nil))
}
