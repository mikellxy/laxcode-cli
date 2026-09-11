package session

import (
	"context"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// SessionRepository 是会话持久化端口：domain 只声明“要存什么、要读回什么”，
// 数据库表、事务和本地冷备由 infrastructure/sessionrepo 决定。
// 加载与写回由 application 层编排，聚合自身不持有本端口。
//
// 运行时只读写最新 RequestContext。仓储负责在同一数据库事务内更新工作集和
// 不可变原始历史；history.jsonl 只是提交后的 best-effort 本地冷备。
type SessionRepository interface {
	// GetRequestContext 读取数据库中的最新工作集；会话不存在时返回空工作集。
	GetRequestContext(ctx context.Context, sessionID string) (RequestContext, error)
	// SaveCheckpoint 保存不产生新 original 消息的工作集变更。新 Session 只能
	// 通过该方法以 Revision=0、LastSeq=0 初始化；成功后 revision 仍会递增。
	// 参数在同步调用期间只读借用。实现不得修改或在返回后保留任何切片、
	// 指针引用；内存存储或异步处理须自行复制。调用方在返回前也不得修改参数。
	SaveCheckpoint(ctx context.Context, sessionID string, snapshot RequestContext) (uint64, error)
	// CommitAppendedMessage 原子保存一条明确的新增 original 消息及其对应的最新
	// 工作集。snapshot 必须只比当前状态前进一步，且尾消息必须与 newMsg 等价。
	CommitAppendedMessage(ctx context.Context, sessionID string, snapshot RequestContext, newMsg sharedkernel.Message) (uint64, error)
}
