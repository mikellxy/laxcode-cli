package sessionrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/libtnb/sqlite"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	messageKindOriginal  = "original"
	messageKindCompacted = "compacted"
	messageKindSystem    = "system"
)

var (
	ErrContextConflict = errors.New("sessionrepo: request context revision conflict")
	ErrStaleSequence   = errors.New("sessionrepo: stale message sequence")
)

type sessionContextModel struct {
	SessionID         string    `gorm:"column:session_id;type:varchar(128);primaryKey"`
	ContextVersion    int       `gorm:"column:context_version;not null"`
	Revision          uint64    `gorm:"column:revision;not null"`
	LastSeq           uint64    `gorm:"column:last_seq;not null"`
	ActiveChatID      string    `gorm:"column:active_chat_id;type:varchar(128);not null"`
	TokenUsedInput    int64     `gorm:"column:token_used_input;not null"`
	TokenUsedOutput   int64     `gorm:"column:token_used_output;not null"`
	WindowTokenInput  int64     `gorm:"column:window_token_input;not null"`
	WindowTokenOutput int64     `gorm:"column:window_token_output;not null"`
	CreatedAt         time.Time `gorm:"column:created_at;not null"`
	UpdatedAt         time.Time `gorm:"column:updated_at;not null"`
}

func (sessionContextModel) TableName() string { return "session_contexts" }

type messageModel struct {
	MessageID        string    `gorm:"column:message_id;type:varchar(36);primaryKey"`
	SessionID        string    `gorm:"column:session_id;type:varchar(128);not null;index:idx_messages_session_role,priority:1;index:idx_messages_session_turn,priority:1;index:idx_messages_tool_group,priority:1"`
	Seq              uint64    `gorm:"column:seq;not null"`
	MessageKind      string    `gorm:"column:message_kind;type:varchar(32);not null"`
	OriginMessageID  *string   `gorm:"column:origin_message_id;type:varchar(36);index"`
	Role             string    `gorm:"column:role;type:varchar(32);not null;index:idx_messages_session_role,priority:2"`
	TurnID           string    `gorm:"column:turn_id;type:varchar(128);not null;index:idx_messages_session_turn,priority:2"`
	ToolCallGroupID  string    `gorm:"column:tool_call_group_id;type:varchar(128);not null;index:idx_messages_tool_group,priority:2"`
	ToolCallID       string    `gorm:"column:tool_call_id;type:varchar(128);not null"`
	Content          string    `gorm:"column:content;type:text;not null"`
	ReasoningID      string    `gorm:"column:reasoning_id;type:varchar(255);not null"`
	ReasoningContent string    `gorm:"column:reasoning_content;type:text;not null"`
	ToolCallsJSON    []byte    `gorm:"column:tool_calls_json;type:json"`
	ArtifactID       *string   `gorm:"column:artifact_id;type:varchar(64)"`
	ArtifactByteSize *int64    `gorm:"column:artifact_byte_size"`
	TokenInput       int64     `gorm:"column:token_input;not null"`
	TokenOutput      int64     `gorm:"column:token_output;not null"`
	PayloadHash      string    `gorm:"column:payload_hash;type:varchar(64);not null;index"`
	CreatedAt        time.Time `gorm:"column:created_at;not null"`
}

func (messageModel) TableName() string { return "messages" }

type sessionContextMessageModel struct {
	SessionID string `gorm:"column:session_id;type:varchar(128);primaryKey;uniqueIndex:idx_context_message,priority:1"`
	Position  int    `gorm:"column:position;primaryKey"`
	MessageID string `gorm:"column:message_id;type:varchar(36);not null;uniqueIndex:idx_context_message,priority:2"`
}

func (sessionContextMessageModel) TableName() string { return "session_context_messages" }

type historyEntryModel struct {
	MessageID    string    `gorm:"column:message_id;type:varchar(36);primaryKey"`
	SessionID    string    `gorm:"column:session_id;type:varchar(128);not null;uniqueIndex:idx_history_session_seq,priority:1"`
	Seq          uint64    `gorm:"column:seq;not null;uniqueIndex:idx_history_session_seq,priority:2"`
	ActiveChatID string    `gorm:"column:active_chat_id;type:varchar(128);not null"`
	CommittedAt  time.Time `gorm:"column:committed_at;not null"`
}

func (historyEntryModel) TableName() string { return "history_entries" }

type schemaMigrationModel struct {
	Version   int       `gorm:"column:version;primaryKey"`
	AppliedAt time.Time `gorm:"column:applied_at;not null"`
}

func (schemaMigrationModel) TableName() string { return "schema_migrations" }

// SqliteSessionRepo 将当前工作集和不可变历史保存在 SQLite。historyRoot 只用于
// commit 后的 JSONL 冷备，写入失败不会改变数据库提交结果。
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

func (r *SqliteSessionRepo) migrate() error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at DATETIME NOT NULL
		)`).Error; err != nil {
			return fmt.Errorf("create migration table: %w", err)
		}
		var count int64
		if err := tx.Model(&schemaMigrationModel{}).Where("version = ?", 1).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return nil
		}
		statements := []string{
			`CREATE TABLE session_contexts (
				session_id VARCHAR(128) PRIMARY KEY NOT NULL,
				context_version INTEGER NOT NULL,
				revision BIGINT NOT NULL,
				last_seq BIGINT NOT NULL CHECK (last_seq >= 0),
				active_chat_id VARCHAR(128) NOT NULL DEFAULT '',
				token_used_input BIGINT NOT NULL DEFAULT 0,
				token_used_output BIGINT NOT NULL DEFAULT 0,
				window_token_input BIGINT NOT NULL DEFAULT 0,
				window_token_output BIGINT NOT NULL DEFAULT 0,
				created_at DATETIME NOT NULL,
				updated_at DATETIME NOT NULL
			)`,
			`CREATE TABLE messages (
				message_id VARCHAR(36) PRIMARY KEY NOT NULL,
				session_id VARCHAR(128) NOT NULL,
				seq BIGINT NOT NULL CHECK (seq >= 0),
				message_kind VARCHAR(32) NOT NULL CHECK (message_kind IN ('original', 'compacted', 'system')),
				origin_message_id VARCHAR(36),
				role VARCHAR(32) NOT NULL,
				turn_id VARCHAR(128) NOT NULL DEFAULT '',
				tool_call_group_id VARCHAR(128) NOT NULL DEFAULT '',
				tool_call_id VARCHAR(128) NOT NULL DEFAULT '',
				content TEXT NOT NULL,
				reasoning_id VARCHAR(255) NOT NULL DEFAULT '',
				reasoning_content TEXT NOT NULL,
				tool_calls_json JSON,
				artifact_id VARCHAR(64),
				artifact_byte_size BIGINT,
				token_input BIGINT NOT NULL DEFAULT 0,
				token_output BIGINT NOT NULL DEFAULT 0,
				payload_hash VARCHAR(64) NOT NULL,
				created_at DATETIME NOT NULL,
				FOREIGN KEY (session_id) REFERENCES session_contexts(session_id) ON UPDATE CASCADE ON DELETE CASCADE,
				FOREIGN KEY (origin_message_id) REFERENCES messages(message_id) ON UPDATE CASCADE ON DELETE RESTRICT
			)`,
			`CREATE TABLE session_context_messages (
				session_id VARCHAR(128) NOT NULL,
				position BIGINT NOT NULL,
				message_id VARCHAR(36) NOT NULL,
				PRIMARY KEY (session_id, position),
				UNIQUE (session_id, message_id),
				FOREIGN KEY (session_id) REFERENCES session_contexts(session_id) ON UPDATE CASCADE ON DELETE CASCADE,
				FOREIGN KEY (message_id) REFERENCES messages(message_id) ON UPDATE CASCADE ON DELETE CASCADE
			)`,
			`CREATE TABLE history_entries (
				message_id VARCHAR(36) PRIMARY KEY NOT NULL,
				session_id VARCHAR(128) NOT NULL,
				seq BIGINT NOT NULL,
				active_chat_id VARCHAR(128) NOT NULL DEFAULT '',
				committed_at DATETIME NOT NULL,
				UNIQUE (session_id, seq),
				FOREIGN KEY (message_id) REFERENCES messages(message_id) ON UPDATE CASCADE ON DELETE CASCADE,
				FOREIGN KEY (session_id) REFERENCES session_contexts(session_id) ON UPDATE CASCADE ON DELETE CASCADE
			)`,
			`CREATE INDEX idx_messages_session_role ON messages(session_id, role)`,
			`CREATE INDEX idx_messages_session_turn ON messages(session_id, turn_id)`,
			`CREATE INDEX idx_messages_tool_group ON messages(session_id, tool_call_group_id)`,
			`CREATE INDEX idx_messages_origin ON messages(origin_message_id)`,
			`CREATE INDEX idx_messages_payload_hash ON messages(payload_hash)`,
		}
		for _, statement := range statements {
			if err := tx.Exec(statement).Error; err != nil {
				return fmt.Errorf("migrate session schema v1: %w", err)
			}
		}
		return tx.Create(&schemaMigrationModel{Version: 1, AppliedAt: time.Now().UTC()}).Error
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
		var err error
		snapshot, err = r.getRequestContext(tx, id)
		return err
	})
	return snapshot, err
}

func (r *SqliteSessionRepo) getRequestContext(db *gorm.DB, id string) (session.RequestContext, error) {
	var state sessionContextModel
	err := db.Where("session_id = ?", id).Take(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return session.RequestContext{Version: session.RequestContextVersion}, nil
	}
	if err != nil {
		return session.RequestContext{}, fmt.Errorf("load session context: %w", err)
	}
	var rows []messageModel
	err = db.
		Table("messages AS m").
		Select("m.*").
		Joins("JOIN session_context_messages AS scm ON scm.message_id = m.message_id").
		Where("scm.session_id = ?", id).
		Order("scm.position ASC").
		Scan(&rows).Error
	if err != nil {
		return session.RequestContext{}, fmt.Errorf("load session messages: %w", err)
	}
	msgs := make([]sharedkernel.Message, 0, len(rows))
	for i := range rows {
		msg, err := modelToMessage(rows[i])
		if err != nil {
			return session.RequestContext{}, fmt.Errorf("decode context message %s: %w", rows[i].MessageID, err)
		}
		msgs = append(msgs, msg)
	}
	snapshot := session.RequestContext{
		Revision:     state.Revision,
		Version:      state.ContextVersion,
		LastSeq:      state.LastSeq,
		ActiveChatID: state.ActiveChatID,
		Messages:     msgs,
		TokenUsed: sharedkernel.TokenStatistics{
			TokenInput: int(state.TokenUsedInput), TokenOutput: int(state.TokenUsedOutput),
		},
		WindowToken: sharedkernel.TokenStatistics{
			TokenInput: int(state.WindowTokenInput), TokenOutput: int(state.WindowTokenOutput),
		},
	}
	if err := snapshot.Validate(); err != nil {
		return snapshot, fmt.Errorf("validate stored request context: %w", err)
	}
	return snapshot, nil
}

type commitKind uint8

const (
	commitSnapshot commitKind = iota
	commitAppend
)

func (r *SqliteSessionRepo) CommitSnapshot(
	ctx context.Context,
	id string,
	snapshot session.RequestContext,
) (uint64, error) {
	return r.commit(ctx, id, snapshot, commitSnapshot, nil)
}

func (r *SqliteSessionRepo) CommitAppendedMessage(
	ctx context.Context,
	id string,
	snapshot session.RequestContext,
	newMsg sharedkernel.Message,
) (uint64, error) {
	return r.commit(ctx, id, snapshot, commitAppend, &newMsg)
}

func (r *SqliteSessionRepo) commit(
	ctx context.Context,
	id string,
	snapshot session.RequestContext,
	kind commitKind,
	newMsg *sharedkernel.Message,
) (uint64, error) {
	if err := validSessionID(id); err != nil {
		return 0, err
	}
	if err := snapshot.Validate(); err != nil {
		return 0, err
	}
	var backup []sharedkernel.Message
	newRevision := snapshot.Revision + 1
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, exists, err := loadCurrentContext(tx, id)
		if err != nil {
			return err
		}
		if err := validateCommit(kind, snapshot, current, exists, newMsg); err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := writeSessionState(tx, id, snapshot, newRevision, now, exists); err != nil {
			return err
		}

		originals, err := loadOriginalMessages(tx, id)
		if err != nil {
			return err
		}
		if kind == commitAppend {
			row, backupMsg, err := appendOriginal(tx, id, snapshot, current, *newMsg)
			if err != nil {
				return err
			}
			originals[newMsg.Seq] = row
			backup = append(backup, backupMsg)
		}

		links, err := r.resolveContextMessages(tx, id, snapshot.Messages, originals)
		if err != nil {
			return err
		}
		return replaceContextLinks(tx, id, links)
	})
	if err != nil {
		return 0, err
	}
	if len(backup) > 0 {
		if err := appendHistory(r.historyRoot, id, backup); err != nil {
			slog.WarnContext(ctx, "session_history_backup_failed",
				"session_id", id, "message_count", len(backup), "error", err)
		}
	}
	return newRevision, nil
}

func loadCurrentContext(tx *gorm.DB, id string) (sessionContextModel, bool, error) {
	var current sessionContextModel
	err := tx.Where("session_id = ?", id).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sessionContextModel{}, false, nil
	}
	if err != nil {
		return sessionContextModel{}, false, fmt.Errorf("load current session context: %w", err)
	}
	return current, true, nil
}

func validateCommit(
	kind commitKind,
	snapshot session.RequestContext,
	current sessionContextModel,
	exists bool,
	newMsg *sharedkernel.Message,
) error {
	if !exists {
		if kind != commitSnapshot {
			return fmt.Errorf("%w: appended message requires an existing session", ErrContextConflict)
		}
		if snapshot.Revision != 0 {
			return fmt.Errorf("%w: got=%d want=0", ErrContextConflict, snapshot.Revision)
		}
		if snapshot.LastSeq != 0 {
			return fmt.Errorf("%w: new snapshot last_seq=%d want=0", ErrStaleSequence, snapshot.LastSeq)
		}
		for i := range snapshot.Messages {
			if snapshot.Messages[i].Role != sharedkernel.RoleSystem {
				return fmt.Errorf("%w: new snapshot contains a non-system message", ErrStaleSequence)
			}
		}
		return nil
	}

	if current.Revision != snapshot.Revision {
		return fmt.Errorf("%w: got=%d want=%d", ErrContextConflict, snapshot.Revision, current.Revision)
	}
	switch kind {
	case commitSnapshot:
		if snapshot.LastSeq != current.LastSeq {
			return fmt.Errorf("%w: snapshot last_seq=%d current=%d", ErrStaleSequence, snapshot.LastSeq, current.LastSeq)
		}
		if newMsg != nil {
			return fmt.Errorf("%w: snapshot includes an appended message", ErrStaleSequence)
		}
	case commitAppend:
		if current.LastSeq == ^uint64(0) || snapshot.LastSeq != current.LastSeq+1 {
			return fmt.Errorf("%w: append last_seq=%d current=%d", ErrStaleSequence, snapshot.LastSeq, current.LastSeq)
		}
		if newMsg == nil || len(snapshot.Messages) == 0 {
			return fmt.Errorf("%w: appended message is missing", ErrStaleSequence)
		}
		if newMsg.Role == sharedkernel.RoleSystem || newMsg.Seq != snapshot.LastSeq {
			return fmt.Errorf("%w: invalid appended message", ErrStaleSequence)
		}
		tail := snapshot.Messages[len(snapshot.Messages)-1]
		if !equalMessage(tail, *newMsg) {
			return fmt.Errorf("%w: appended message does not match snapshot tail", ErrStaleSequence)
		}
		isFinalAssistant := newMsg.Role == sharedkernel.RoleAssistant && len(newMsg.ToolCalls) == 0
		if isFinalAssistant && snapshot.ActiveChatID != "" {
			return fmt.Errorf("%w: final assistant must clear active chat", ErrStaleSequence)
		}
		if !isFinalAssistant && snapshot.ActiveChatID == "" {
			return fmt.Errorf("%w: appended message requires an active chat", ErrStaleSequence)
		}
	default:
		return fmt.Errorf("sessionrepo: unknown commit kind %d", kind)
	}
	return nil
}

func writeSessionState(
	tx *gorm.DB,
	id string,
	snapshot session.RequestContext,
	newRevision uint64,
	now time.Time,
	exists bool,
) error {
	state := contextToModel(id, snapshot, newRevision, now)
	if !exists {
		state.CreatedAt = now
		return tx.Create(&state).Error
	}
	result := tx.Model(&sessionContextModel{}).
		Where("session_id = ? AND revision = ?", id, snapshot.Revision).
		Updates(map[string]any{
			"context_version":     state.ContextVersion,
			"revision":            state.Revision,
			"last_seq":            state.LastSeq,
			"active_chat_id":      state.ActiveChatID,
			"token_used_input":    state.TokenUsedInput,
			"token_used_output":   state.TokenUsedOutput,
			"window_token_input":  state.WindowTokenInput,
			"window_token_output": state.WindowTokenOutput,
			"updated_at":          state.UpdatedAt,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrContextConflict
	}
	return nil
}

func appendOriginal(
	tx *gorm.DB,
	id string,
	snapshot session.RequestContext,
	current sessionContextModel,
	newMsg sharedkernel.Message,
) (messageModel, sharedkernel.Message, error) {
	row, err := createMessage(tx, id, messageKindOriginal, nil, newMsg)
	if err != nil {
		return messageModel{}, sharedkernel.Message{}, err
	}
	historyChatID := snapshot.ActiveChatID
	if newMsg.Role == sharedkernel.RoleAssistant && len(newMsg.ToolCalls) == 0 &&
		historyChatID == "" && current.ActiveChatID != "" {
		historyChatID = current.ActiveChatID
	}
	if err := createHistoryEntry(tx, id, row.MessageID, historyChatID, newMsg.Seq); err != nil {
		return messageModel{}, sharedkernel.Message{}, err
	}
	return row, newMsg.Clone(), nil
}

func (r *SqliteSessionRepo) resolveContextMessages(
	tx *gorm.DB,
	id string,
	messages []sharedkernel.Message,
	originals map[uint64]messageModel,
) ([]sessionContextMessageModel, error) {
	links := make([]sessionContextMessageModel, 0, len(messages))
	for position, msg := range messages {
		row, err := r.resolveContextMessage(tx, id, msg, originals)
		if err != nil {
			return nil, err
		}
		links = append(links, sessionContextMessageModel{
			SessionID: id, Position: position, MessageID: row.MessageID,
		})
	}
	return links, nil
}

func replaceContextLinks(tx *gorm.DB, id string, links []sessionContextMessageModel) error {
	if err := tx.Where("session_id = ?", id).Delete(&sessionContextMessageModel{}).Error; err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	return tx.Create(&links).Error
}

func contextToModel(id string, snapshot session.RequestContext, revision uint64, now time.Time) sessionContextModel {
	return sessionContextModel{
		SessionID: id, ContextVersion: snapshot.Version, Revision: revision,
		LastSeq: snapshot.LastSeq, ActiveChatID: snapshot.ActiveChatID,
		TokenUsedInput:    int64(snapshot.TokenUsed.TokenInput),
		TokenUsedOutput:   int64(snapshot.TokenUsed.TokenOutput),
		WindowTokenInput:  int64(snapshot.WindowToken.TokenInput),
		WindowTokenOutput: int64(snapshot.WindowToken.TokenOutput),
		UpdatedAt:         now,
	}
}

func loadOriginalMessages(tx *gorm.DB, id string) (map[uint64]messageModel, error) {
	var rows []messageModel
	err := tx.Table("messages AS m").Select("m.*").
		Joins("JOIN history_entries AS h ON h.message_id = m.message_id").
		Where("h.session_id = ?", id).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]messageModel, len(rows))
	for _, row := range rows {
		out[row.Seq] = row
	}
	return out, nil
}

func createHistoryEntry(tx *gorm.DB, sessionID, messageID, activeChatID string, seq uint64) error {
	return tx.Create(&historyEntryModel{
		MessageID: messageID, SessionID: sessionID, Seq: seq,
		ActiveChatID: activeChatID, CommittedAt: time.Now().UTC(),
	}).Error
}

func (r *SqliteSessionRepo) resolveContextMessage(tx *gorm.DB, id string, msg sharedkernel.Message, originals map[uint64]messageModel) (messageModel, error) {
	if msg.Role == sharedkernel.RoleSystem {
		return findOrCreateMessage(tx, id, messageKindSystem, nil, msg)
	}
	original, ok := originals[msg.Seq]
	if !ok {
		return messageModel{}, fmt.Errorf("context message seq %d has no original history row", msg.Seq)
	}
	originalMsg, err := modelToMessage(original)
	if err != nil {
		return messageModel{}, err
	}
	if equalMessage(originalMsg, msg) {
		return original, nil
	}
	originID := original.MessageID
	return findOrCreateMessage(tx, id, messageKindCompacted, &originID, msg)
}

func findOrCreateMessage(tx *gorm.DB, id, kind string, originID *string, msg sharedkernel.Message) (messageModel, error) {
	hash, err := messageHash(msg)
	if err != nil {
		return messageModel{}, err
	}
	q := tx.Where("session_id = ? AND message_kind = ? AND payload_hash = ?", id, kind, hash)
	if originID == nil {
		q = q.Where("origin_message_id IS NULL")
	} else {
		q = q.Where("origin_message_id = ?", *originID)
	}
	var row messageModel
	if err := q.Take(&row).Error; err == nil {
		stored, decodeErr := modelToMessage(row)
		if decodeErr != nil {
			return messageModel{}, decodeErr
		}
		if equalMessage(stored, msg) {
			return row, nil
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return messageModel{}, err
	}
	return createMessage(tx, id, kind, originID, msg)
}

func createMessage(tx *gorm.DB, id, kind string, originID *string, msg sharedkernel.Message) (messageModel, error) {
	row, err := messageToModel(id, kind, originID, msg)
	if err != nil {
		return row, err
	}
	if err := tx.Create(&row).Error; err != nil {
		return row, err
	}
	return row, nil
}

func messageToModel(id, kind string, originID *string, msg sharedkernel.Message) (messageModel, error) {
	toolCalls, err := json.Marshal(msg.ToolCalls)
	if err != nil {
		return messageModel{}, err
	}
	hash, err := messageHash(msg)
	if err != nil {
		return messageModel{}, err
	}
	row := messageModel{
		MessageID: uuid.NewString(), SessionID: id, Seq: msg.Seq,
		MessageKind: kind, OriginMessageID: originID, Role: msg.Role,
		TurnID: msg.TurnID, ToolCallGroupID: msg.ToolCallGroupID,
		ToolCallID: msg.ToolCallID, Content: msg.Content,
		ReasoningID: msg.ReasoningID, ReasoningContent: msg.ReasoningContent,
		ToolCallsJSON: toolCalls, TokenInput: int64(msg.TokenUsed.TokenInput),
		TokenOutput: int64(msg.TokenUsed.TokenOutput), PayloadHash: hash,
		CreatedAt: time.Now().UTC(),
	}
	if msg.Artifact != nil {
		id := msg.Artifact.ID
		size := int64(msg.Artifact.ByteSize)
		row.ArtifactID, row.ArtifactByteSize = &id, &size
	}
	return row, nil
}

func modelToMessage(row messageModel) (sharedkernel.Message, error) {
	var calls []sharedkernel.ToolCall
	if len(row.ToolCallsJSON) != 0 && string(row.ToolCallsJSON) != "null" {
		if err := json.Unmarshal(row.ToolCallsJSON, &calls); err != nil {
			return sharedkernel.Message{}, err
		}
	}
	msg := sharedkernel.Message{
		Seq: row.Seq, TurnID: row.TurnID, ToolCallGroupID: row.ToolCallGroupID,
		Role: row.Role, Content: row.Content, ReasoningID: row.ReasoningID,
		ReasoningContent: row.ReasoningContent, ToolCalls: calls,
		ToolCallID: row.ToolCallID,
		TokenUsed: sharedkernel.TokenStatistics{
			TokenInput: int(row.TokenInput), TokenOutput: int(row.TokenOutput),
		},
	}
	if row.ArtifactID != nil {
		msg.Artifact = &sharedkernel.ArtifactRef{ID: *row.ArtifactID}
		if row.ArtifactByteSize != nil {
			msg.Artifact.ByteSize = int(*row.ArtifactByteSize)
		}
	}
	return msg, nil
}

func messageHash(msg sharedkernel.Message) (string, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func equalMessage(a, b sharedkernel.Message) bool {
	aJSON, errA := json.Marshal(a)
	bJSON, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(aJSON) == string(bJSON)
}

var _ session.SessionRepository = (*SqliteSessionRepo)(nil)
