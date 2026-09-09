package session

import (
	"context"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// SessionRepository 是会话持久化端口：domain 只声明“要存什么、要读回什么”，
// 落盘形态（JSONL、独立文件、目录布局）由 infrastructure/sessionrepo 决定。
// 加载与写回由 application 层编排，聚合自身不持有本端口。
//
// 契约：GetMessages 返回的序列以系统消息为首（若该会话已设置系统提示词），
// 其后是 AppendMessage 写入的对话消息；系统消息只经 UpsertSysMessage 写入，
// 不得经 AppendMessage 追加，否则续聊会读回两条系统提示词。
type SessionRepository interface {
	// AppendMessage 追加一条对话消息（user / assistant / tool）。
	AppendMessage(ctx context.Context, sessionID string, msg *sharedkernel.Message) error
	// UpsertSysMessage 写入或覆盖会话的系统提示词（update or insert）。
	UpsertSysMessage(ctx context.Context, sessionID string, msg *sharedkernel.Message) error
	// UpdateMeta 覆盖写入会话的 token 账目。
	UpdateMeta(ctx context.Context, sessionID string, meta *sharedkernel.SessionMeta) error
	// GetMessages 读回完整消息序列（系统消息居首）；会话不存在时返回空序列而非错误。
	GetMessages(ctx context.Context, sessionID string) ([]sharedkernel.Message, error)
	// GetMeta 读回 token 账目；会话不存在时返回零值而非错误。
	GetMeta(ctx context.Context, sessionID string) (sharedkernel.SessionMeta, error)
}
