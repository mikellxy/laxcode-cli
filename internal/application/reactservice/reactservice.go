package reactservice

import (
	"context"

	"github.com/mikellxy/laxcode/internal/domain/llmprovider"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"github.com/mikellxy/laxcode/internal/domain/tools"
	"github.com/mikellxy/laxcode/internal/infrastructure/compactor"
)

type ReActService struct {
	Session             *session.Session
	LLMClient           llmprovider.LLMClient
	ToolRegistry        tools.Registry
	ReActEventConsumerF func(reactEvent *ReactEvent)
}

const (
	ReActEventTypeMsg       = "msg"
	ReActEventTypeReasoning = "reasoning"
	ReActEventTypeToolCall  = "tool_call"
)

// maxWindowToken 是触发上下文压缩的窗口 token 预算，暂写死 200k，
// 未来再做动态配置。
const maxWindowToken = 200_000

type ReactEvent struct {
	Type    string
	Content string
}

func NewReActService(sess *session.Session,
	llmClient llmprovider.LLMClient,
	toolRegistry tools.Registry,
	reActEventConsumerF func(reactEvent *ReactEvent)) *ReActService {
	return &ReActService{
		Session:             sess,
		LLMClient:           llmClient,
		ToolRegistry:        toolRegistry,
		ReActEventConsumerF: reActEventConsumerF,
	}
}

func (r *ReActService) Run(ctx context.Context) (*sharedkernel.Message, error) {
	for {
		// 上下文压缩：每轮 generate 前压缩历史（对齐老 engine.Run），触发
		// 阈值 maxWindowToken；压缩后回写 Messages 并同步扣减窗口占用。
		msgs, compressRes := compactor.SimpleCompactor.Compress(r.Session.Messages, maxWindowToken, r.Session.TokenUsed)
		r.Session.Messages = msgs
		if compressRes != nil && compressRes.Total() > 0 {
			r.Session.WindowToken.TokenInput -= compressRes.InputTokenCompressed
			r.Session.WindowToken.TokenOutput -= compressRes.OutputTokenCompressed
		}

		msg, err := r.LLMClient.Generate(ctx, r.Session.Messages, r.ToolRegistry.GetAvailableTools())
		if err != nil {
			return nil, err
		}
		if err := r.Session.AppendMessage(ctx, msg); err != nil {
			return nil, err
		}
		if msg.ReasoningContent != "" {
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeReasoning, Content: msg.ReasoningContent})
		}
		if msg.Content != "" {
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeMsg, Content: msg.Content})
		}

		// 无工具调用，推理循环完成
		if len(msg.ToolCalls) == 0 {
			return msg, nil
		}

		for _, tc := range msg.ToolCalls {
			info := r.ToolRegistry.BeforeExecInfo(&tc)
			r.ReActEventConsumerF(&ReactEvent{Type: ReActEventTypeToolCall, Content: info})

			result := r.ToolRegistry.Execute(ctx, &tc)
			toolMsg := tools.ToolResultAsMsg(result)
			if err := r.Session.AppendMessage(ctx, toolMsg); err != nil {
				return nil, err
			}
		}
	}
}
