package session

import (
	"context"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// SessionRepository 是会话持久化端口：domain 只声明“要存什么、要读回什么”，
// 落盘形态（JSONL、独立文件、目录布局）由 infrastructure/sessionrepo 决定。
// 加载与写回由 application 层编排，聚合自身不持有本端口。
//
// 运行时只读写最新 RequestContext。原文追加和旧格式迁移由仓储实现负责，
// application 不再分别更新 history、system prompt 和 token meta。
type SessionRepository interface {
	// GetRequestContext 读取最新工作集；无快照的旧会话只迁移一次原始历史。
	GetRequestContext(ctx context.Context, sessionID string) (RequestContext, error)
	// SaveRequestContext 提交工作集。original 非 nil 时同时追加原始消息，
	// 仓储必须支持中断恢复；original 为 nil 用于压缩和系统提示词更新。
	// 参数在同步调用期间只读借用。实现不得修改或在返回后保留任何切片、
	// 指针引用；内存存储或异步处理须自行复制。调用方在返回前也不得修改参数。
	SaveRequestContext(ctx context.Context, sessionID string, snapshot RequestContext, original *sharedkernel.Message) error
}
