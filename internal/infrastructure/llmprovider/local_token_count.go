package llmprovider

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	tiktoken "github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

// 本地兜底计数使用 cl100k_base 编码：它是 OpenAI 系模型的通用 BPE，对多数
// 兼容端点（含 DeepSeek）都能给出量级正确的估算。编码对象全局惰性加载一次，
// 后续复用；离线 loader 内嵌 BPE 文件，运行期不再联网下载。
const localEncodingName = "cl100k_base"

var (
	localEncodingOnce sync.Once
	localEncoding     *tiktoken.Tiktoken
	localEncodingErr  error
)

// getLocalEncoding 惰性装配全局 tiktoken 编码，并发安全且只初始化一次。
func getLocalEncoding() (*tiktoken.Tiktoken, error) {
	localEncodingOnce.Do(func() {
		tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
		localEncoding, localEncodingErr = tiktoken.GetEncoding(localEncodingName)
	})
	if localEncodingErr != nil {
		return nil, fmt.Errorf("load tiktoken encoding %q: %w", localEncodingName, localEncodingErr)
	}
	return localEncoding, nil
}

// countInputTokensLocal 在远端计数端点不可用时提供本地兜底估算。部分
// OpenAI 兼容端点（例如 DeepSeek）并未实现 /responses/input_tokens，直接
// 调用会 404；此时复用 buildResponseParams 的序列化结果做 JSON 编码，再用
// tiktoken 计数。口径与真正发送的请求一致，估算值偏保守（含 JSON 结构
// 开销），足以驱动 compactContext 的高低水位压缩决策。
func (p *OpenApiProvider) countInputTokensLocal(msgs []sharedkernel.Message, toolsDefs []sharedkernel.ToolDefinition) (int, error) {
	encoding, err := getLocalEncoding()
	if err != nil {
		return 0, err
	}
	params := p.buildResponseParams(msgs, toolsDefs)
	data, err := json.Marshal(params)
	if err != nil {
		return 0, fmt.Errorf("marshal params for local token count: %w", err)
	}
	return len(encoding.Encode(string(data), nil, nil)), nil
}
