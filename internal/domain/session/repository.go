package session

import (
	"context"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

type SessionRepository interface {
	AppendMessage(ctx context.Context, sessionID string, msg *sharedkernel.Message) error
	UpdateMeta(ctx context.Context, sessionID string, msg *sharedkernel.SessionMeta) error
	GetMessages(ctx context.Context, sessionID string) ([]sharedkernel.Message, error)
	GetMeta(ctx context.Context, sessionID string) (*sharedkernel.SessionMeta, error)
}
