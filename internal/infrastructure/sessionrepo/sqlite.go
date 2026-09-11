package sessionrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libtnb/sqlite"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	messageTypeOriginal = "original"
	messageTypeMemory   = "in_memory"
)

var (
	ErrContextConflict = errors.New("sessionrepo: request context revision conflict")
	ErrStaleSequence   = errors.New("sessionrepo: stale message sequence")
	ErrStaleGeneration = errors.New("sessionrepo: stale memory generation")
)

type requestContextModel struct {
	SessionID         string    `gorm:"column:session_id;type:varchar(128);primaryKey"`
	Revision          uint64    `gorm:"column:revision;not null"`
	MemoryGeneration  uint64    `gorm:"column:memory_generation;not null"`
	LastSeq           uint64    `gorm:"column:last_seq;not null"`
	TokenUsedInput    int64     `gorm:"column:token_used_input;not null"`
	TokenUsedOutput   int64     `gorm:"column:token_used_output;not null"`
	WindowTokenInput  int64     `gorm:"column:window_token_input;not null"`
	WindowTokenOutput int64     `gorm:"column:window_token_output;not null"`
	CreatedAt         time.Time `gorm:"column:created_at;not null"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null"`
}

func (requestContextModel) TableName() string { return "request_contexts" }

// messageModel 同时承载不可变 original 与各代工作集消息。复合主键使 original
// 和每一代 memory 在同一 session/seq 下各有且只有一条，无需额外关联表。
type messageModel struct {
	SessionID        string    `gorm:"column:session_id;type:varchar(128);primaryKey;priority:1"`
	MessageType      string    `gorm:"column:message_type;type:varchar(32);primaryKey;priority:2"`
	MemoryGeneration uint64    `gorm:"column:memory_generation;primaryKey;priority:3"`
	Seq              uint64    `gorm:"column:seq;primaryKey;priority:4"`
	OriginalSeqJSON  []byte    `gorm:"column:original_seq_json;type:json;not null"`
	Role             string    `gorm:"column:role;type:varchar(32);not null"`
	ToolCallID       string    `gorm:"column:tool_call_id;type:varchar(128);not null"`
	Content          string    `gorm:"column:content;type:text;not null"`
	ReasoningID      string    `gorm:"column:reasoning_id;type:varchar(255);not null"`
	ReasoningContent string    `gorm:"column:reasoning_content;type:text;not null"`
	ToolCallsJSON    []byte    `gorm:"column:tool_calls_json;type:json"`
	ArtifactID       *string   `gorm:"column:artifact_id;type:varchar(64)"`
	ArtifactByteSize *int64    `gorm:"column:artifact_byte_size"`
	TokenInput       int64     `gorm:"column:token_input;not null"`
	TokenOutput      int64     `gorm:"column:token_output;not null"`
	CreatedAt        time.Time `gorm:"column:created_at;not null"`
	UpdatedAt        time.Time `gorm:"column:updated_at;not null"`
}

func (messageModel) TableName() string { return "messages" }

// SqliteSessionRepo 以两张表保存当前 context head、不可变 original 和按代封存
// 的 memory。historyRoot 只用于事务提交后的 best-effort JSONL 冷备。
type SqliteSessionRepo struct {
	db          *gorm.DB
	historyRoot string
}

func NewSqliteSessionRepo(dbPath, historyRoot string) (*SqliteSessionRepo, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	dsn := dbPath + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open session database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get session database handle: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	r := &SqliteSessionRepo{db: db, historyRoot: historyRoot}
	if err := r.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return r, nil
}

// 无老数据兼容要求，当前 schema 直接以最终形态创建；业务数据只使用两张表。
func (r *SqliteSessionRepo) migrate() error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		statements := []string{
			`CREATE TABLE IF NOT EXISTS request_contexts (
				session_id VARCHAR(128) PRIMARY KEY NOT NULL,
				revision BIGINT NOT NULL CHECK (revision >= 0),
				memory_generation BIGINT NOT NULL CHECK (memory_generation >= 1),
				last_seq BIGINT NOT NULL CHECK (last_seq >= 0),
				token_used_input BIGINT NOT NULL DEFAULT 0,
				token_used_output BIGINT NOT NULL DEFAULT 0,
				window_token_input BIGINT NOT NULL DEFAULT 0,
				window_token_output BIGINT NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS messages (
				session_id VARCHAR(128) NOT NULL,
				message_type VARCHAR(32) NOT NULL CHECK (message_type IN ('original', 'in_memory')),
				memory_generation BIGINT NOT NULL CHECK (memory_generation >= 0),
				seq BIGINT NOT NULL CHECK (seq >= 1),
				original_seq_json JSON NOT NULL CHECK (json_valid(original_seq_json) AND json_array_length(original_seq_json) >= 1),
				role VARCHAR(32) NOT NULL,
				tool_call_id VARCHAR(128) NOT NULL DEFAULT '',
				content TEXT NOT NULL,
				reasoning_id VARCHAR(255) NOT NULL DEFAULT '',
				reasoning_content TEXT NOT NULL DEFAULT '',
				tool_calls_json JSON,
				artifact_id VARCHAR(64),
				artifact_byte_size BIGINT,
				token_input BIGINT NOT NULL DEFAULT 0,
				token_output BIGINT NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL,
				PRIMARY KEY (session_id, message_type, memory_generation, seq),
				CHECK ((message_type = 'original' AND memory_generation = 0
						AND json_array_length(original_seq_json) = 1
						AND json_extract(original_seq_json, '$[0]') = seq)
					OR (message_type = 'in_memory' AND memory_generation >= 1)),
				FOREIGN KEY (session_id) REFERENCES request_contexts(session_id) ON UPDATE CASCADE ON DELETE CASCADE
			)`,
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("create session schema: %w", err)
			}
		}
		return nil
	})
}

func (r *SqliteSessionRepo) Close() error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func validSessionID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return fmt.Errorf("invalid session ID")
	}
	return nil
}

func (r *SqliteSessionRepo) GetRequestContext(ctx context.Context, id string) (session.RequestContext, error) {
	if err := validSessionID(id); err != nil {
		return session.RequestContext{}, err
	}
	var snapshot session.RequestContext
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state requestContextModel
		err := tx.Where("session_id = ?", id).Take(&state).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			snapshot = session.RequestContext{MemoryGeneration: 1}
			return nil
		}
		if err != nil {
			return fmt.Errorf("load request context: %w", err)
		}
		var rows []messageModel
		if err := tx.Where("session_id = ? AND message_type = ? AND memory_generation = ?",
			id, messageTypeMemory, state.MemoryGeneration).Order("seq ASC").Find(&rows).Error; err != nil {
			return fmt.Errorf("load memory messages: %w", err)
		}
		msgs := make([]sharedkernel.Message, 0, len(rows))
		for i := range rows {
			msg, err := modelToMessage(rows[i])
			if err != nil {
				return fmt.Errorf("decode memory message %d: %w", rows[i].Seq, err)
			}
			msgs = append(msgs, msg)
		}
		snapshot = contextFromModel(state, msgs)
		return snapshot.Validate()
	})
	return snapshot, err
}

func (r *SqliteSessionRepo) CommitCreateMessage(ctx context.Context, id string, snapshot session.RequestContext, original, memory sharedkernel.Message) (uint64, error) {
	if err := validSessionID(id); err != nil {
		return 0, err
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	if err := validateCreatedMessage(snapshot, original, memory); err != nil {
		return 0, err
	}
	if snapshot.Revision == ^uint64(0) {
		return 0, fmt.Errorf("%w: revision exhausted", ErrContextConflict)
	}
	newRevision := snapshot.Revision + 1
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, exists, err := loadCurrentContext(tx, id)
		if err != nil {
			return err
		}
		if err := validateCreateTransition(snapshot, current, exists); err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := writeContext(tx, id, snapshot, newRevision, now, exists); err != nil {
			return err
		}
		originalRow, err := messageToModel(id, messageTypeOriginal, 0, original, now)
		if err != nil {
			return err
		}
		memoryRow, err := messageToModel(id, messageTypeMemory, snapshot.MemoryGeneration, memory, now)
		if err != nil {
			return err
		}
		return tx.Create(&[]messageModel{originalRow, memoryRow}).Error
	})
	if err != nil {
		return 0, err
	}
	if err := appendHistory(r.historyRoot, id, []sharedkernel.Message{original.Clone()}); err != nil {
		slog.WarnContext(ctx, "session_history_backup_failed", "session_id", id, "message_count", 1, "error", err)
	}
	return newRevision, nil
}

func (r *SqliteSessionRepo) CommitUpdateMessage(ctx context.Context, id string, snapshot session.RequestContext, memory sharedkernel.Message) (uint64, error) {
	if err := validSessionID(id); err != nil {
		return 0, err
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	if !snapshotContains(snapshot.Messages, memory) {
		return 0, fmt.Errorf("%w: updated memory is absent from snapshot", ErrStaleSequence)
	}
	if snapshot.Revision == ^uint64(0) {
		return 0, fmt.Errorf("%w: revision exhausted", ErrContextConflict)
	}
	newRevision := snapshot.Revision + 1
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, exists, err := loadCurrentContext(tx, id)
		if err != nil {
			return err
		}
		if !exists || current.Revision != snapshot.Revision {
			return ErrContextConflict
		}
		if current.LastSeq != snapshot.LastSeq {
			return ErrStaleSequence
		}
		if current.MemoryGeneration != snapshot.MemoryGeneration {
			return ErrStaleGeneration
		}
		now := time.Now().UTC()
		row, err := messageToModel(id, messageTypeMemory, snapshot.MemoryGeneration, memory, now)
		if err != nil {
			return err
		}
		result := tx.Model(&messageModel{}).Where(
			"session_id = ? AND message_type = ? AND memory_generation = ? AND seq = ?",
			id, messageTypeMemory, snapshot.MemoryGeneration, memory.Seq,
		).Updates(messagePayload(row))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: memory message %d does not exist", ErrStaleSequence, memory.Seq)
		}
		return writeContext(tx, id, snapshot, newRevision, now, true)
	})
	if err != nil {
		return 0, err
	}
	return newRevision, nil
}

func (r *SqliteSessionRepo) CommitNextMemoryGeneration(ctx context.Context, id string, snapshot session.RequestContext) (uint64, error) {
	if err := validSessionID(id); err != nil {
		return 0, err
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	if snapshot.Revision == ^uint64(0) {
		return 0, fmt.Errorf("%w: revision exhausted", ErrContextConflict)
	}
	newRevision := snapshot.Revision + 1
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, exists, err := loadCurrentContext(tx, id)
		if err != nil {
			return err
		}
		if !exists || current.Revision != snapshot.Revision {
			return ErrContextConflict
		}
		if current.LastSeq != snapshot.LastSeq {
			return ErrStaleSequence
		}
		if current.MemoryGeneration == ^uint64(0) || snapshot.MemoryGeneration != current.MemoryGeneration+1 {
			return ErrStaleGeneration
		}
		now := time.Now().UTC()
		rows := make([]messageModel, 0, len(snapshot.Messages))
		for _, msg := range snapshot.Messages {
			row, err := messageToModel(id, messageTypeMemory, snapshot.MemoryGeneration, msg, now)
			if err != nil {
				return err
			}
			rows = append(rows, row)
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		return writeContext(tx, id, snapshot, newRevision, now, true)
	})
	if err != nil {
		return 0, err
	}
	return newRevision, nil
}

func loadCurrentContext(tx *gorm.DB, id string) (requestContextModel, bool, error) {
	var current requestContextModel
	err := tx.Where("session_id = ?", id).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return requestContextModel{}, false, nil
	}
	if err != nil {
		return requestContextModel{}, false, fmt.Errorf("load current request context: %w", err)
	}
	return current, true, nil
}

func validateCreatedMessage(snapshot session.RequestContext, original, memory sharedkernel.Message) error {
	if original.Seq == 0 || original.Seq != snapshot.LastSeq ||
		len(original.OriginalSeq) != 1 || original.OriginalSeq[0] != original.Seq {
		return fmt.Errorf("%w: invalid original identity", ErrStaleSequence)
	}
	if !equalMessage(original, memory) {
		return fmt.Errorf("%w: original and initial memory differ", ErrStaleSequence)
	}
	if len(snapshot.Messages) == 0 || !equalMessage(snapshot.Messages[len(snapshot.Messages)-1], memory) {
		return fmt.Errorf("%w: memory does not match snapshot tail", ErrStaleSequence)
	}
	return nil
}

func validateCreateTransition(snapshot session.RequestContext, current requestContextModel, exists bool) error {
	if !exists {
		if snapshot.Revision != 0 {
			return ErrContextConflict
		}
		if snapshot.MemoryGeneration != 1 {
			return ErrStaleGeneration
		}
		if snapshot.LastSeq != 1 || len(snapshot.Messages) != 1 {
			return ErrStaleSequence
		}
		return nil
	}
	if current.Revision != snapshot.Revision {
		return ErrContextConflict
	}
	if current.MemoryGeneration != snapshot.MemoryGeneration {
		return ErrStaleGeneration
	}
	if current.LastSeq == ^uint64(0) || snapshot.LastSeq != current.LastSeq+1 {
		return ErrStaleSequence
	}
	return nil
}

func writeContext(tx *gorm.DB, id string, snapshot session.RequestContext, revision uint64, now time.Time, exists bool) error {
	state := contextToModel(id, snapshot, revision, now)
	if !exists {
		state.CreatedAt = now
		return tx.Create(&state).Error
	}
	result := tx.Model(&requestContextModel{}).
		Where("session_id = ? AND revision = ?", id, snapshot.Revision).
		Updates(map[string]any{
			"revision": revision, "memory_generation": state.MemoryGeneration,
			"last_seq": state.LastSeq, "token_used_input": state.TokenUsedInput,
			"token_used_output":   state.TokenUsedOutput,
			"window_token_input":  state.WindowTokenInput,
			"window_token_output": state.WindowTokenOutput, "updated_at": now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrContextConflict
	}
	return nil
}

func contextToModel(id string, snapshot session.RequestContext, revision uint64, now time.Time) requestContextModel {
	return requestContextModel{
		SessionID: id, Revision: revision, MemoryGeneration: snapshot.MemoryGeneration,
		LastSeq: snapshot.LastSeq, TokenUsedInput: int64(snapshot.TokenUsed.TokenInput),
		TokenUsedOutput:   int64(snapshot.TokenUsed.TokenOutput),
		WindowTokenInput:  int64(snapshot.WindowToken.TokenInput),
		WindowTokenOutput: int64(snapshot.WindowToken.TokenOutput), UpdatedAt: now,
	}
}

func contextFromModel(state requestContextModel, msgs []sharedkernel.Message) session.RequestContext {
	return session.RequestContext{
		Revision: state.Revision, MemoryGeneration: state.MemoryGeneration, LastSeq: state.LastSeq,
		Messages:    msgs,
		TokenUsed:   sharedkernel.TokenStatistics{TokenInput: int(state.TokenUsedInput), TokenOutput: int(state.TokenUsedOutput)},
		WindowToken: sharedkernel.TokenStatistics{TokenInput: int(state.WindowTokenInput), TokenOutput: int(state.WindowTokenOutput)},
	}
}

func messageToModel(id, messageType string, generation uint64, msg sharedkernel.Message, now time.Time) (messageModel, error) {
	toolCalls, err := json.Marshal(msg.ToolCalls)
	if err != nil {
		return messageModel{}, err
	}
	originalSeq, err := json.Marshal(msg.OriginalSeq)
	if err != nil {
		return messageModel{}, err
	}
	row := messageModel{
		SessionID: id, MessageType: messageType, MemoryGeneration: generation,
		Seq: msg.Seq, OriginalSeqJSON: originalSeq, Role: msg.Role,
		ToolCallID: msg.ToolCallID, Content: msg.Content, ReasoningID: msg.ReasoningID,
		ReasoningContent: msg.ReasoningContent, ToolCallsJSON: toolCalls,
		TokenInput: int64(msg.TokenUsed.TokenInput), TokenOutput: int64(msg.TokenUsed.TokenOutput),
		CreatedAt: now, UpdatedAt: now,
	}
	if msg.Artifact != nil {
		artifactID := msg.Artifact.ID
		artifactSize := int64(msg.Artifact.ByteSize)
		row.ArtifactID, row.ArtifactByteSize = &artifactID, &artifactSize
	}
	return row, nil
}

func messagePayload(row messageModel) map[string]any {
	return map[string]any{
		"original_seq_json": row.OriginalSeqJSON, "role": row.Role, "tool_call_id": row.ToolCallID,
		"content": row.Content, "reasoning_id": row.ReasoningID,
		"reasoning_content": row.ReasoningContent, "tool_calls_json": row.ToolCallsJSON,
		"artifact_id": row.ArtifactID, "artifact_byte_size": row.ArtifactByteSize,
		"token_input": row.TokenInput, "token_output": row.TokenOutput, "updated_at": row.UpdatedAt,
	}
}

func modelToMessage(row messageModel) (sharedkernel.Message, error) {
	var originalSeq []uint64
	if err := json.Unmarshal(row.OriginalSeqJSON, &originalSeq); err != nil {
		return sharedkernel.Message{}, err
	}
	var calls []sharedkernel.ToolCall
	if len(row.ToolCallsJSON) != 0 && string(row.ToolCallsJSON) != "null" {
		if err := json.Unmarshal(row.ToolCallsJSON, &calls); err != nil {
			return sharedkernel.Message{}, err
		}
	}
	msg := sharedkernel.Message{
		Seq: row.Seq, OriginalSeq: originalSeq, Role: row.Role, Content: row.Content,
		ReasoningID: row.ReasoningID, ReasoningContent: row.ReasoningContent,
		ToolCalls: calls, ToolCallID: row.ToolCallID,
		TokenUsed: sharedkernel.TokenStatistics{TokenInput: int(row.TokenInput), TokenOutput: int(row.TokenOutput)},
	}
	if row.ArtifactID != nil {
		msg.Artifact = &sharedkernel.ArtifactRef{ID: *row.ArtifactID}
		if row.ArtifactByteSize != nil {
			msg.Artifact.ByteSize = int(*row.ArtifactByteSize)
		}
	}
	return msg, nil
}

func snapshotContains(messages []sharedkernel.Message, target sharedkernel.Message) bool {
	for _, msg := range messages {
		if msg.Seq == target.Seq {
			return equalMessage(msg, target)
		}
	}
	return false
}

func equalMessage(a, b sharedkernel.Message) bool {
	aJSON, errA := json.Marshal(a)
	bJSON, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(aJSON) == string(bJSON)
}

var _ session.SessionRepository = (*SqliteSessionRepo)(nil)
