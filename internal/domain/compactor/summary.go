package compactor

import (
	"fmt"
	"sort"

	"github.com/mikellxy/laxcode/internal/domain/sharedkernel"
)

// MergeSummary 用一条 user 摘要替换 system 与保护区之间的连续历史。
// system 永远保留；摘要沿用被合并区间最小的 Seq，并聚合全部原始来源。
func MergeSummary(msgs []sharedkernel.Message, protectedStart int, content string) ([]sharedkernel.Message, error) {
	if len(msgs) == 0 || msgs[0].Role != sharedkernel.RoleSystem {
		return nil, fmt.Errorf("compactor: leading system message required")
	}
	if protectedStart <= 1 || protectedStart >= len(msgs) {
		return nil, fmt.Errorf("compactor: invalid summary boundary %d for %d messages", protectedStart, len(msgs))
	}
	if content == "" {
		return nil, fmt.Errorf("compactor: summary content required")
	}

	sources := msgs[1:protectedStart]
	originalSet := make(map[uint64]struct{})
	for _, msg := range sources {
		if len(msg.OriginalSeq) == 0 {
			return nil, fmt.Errorf("compactor: message %d has no original sequence", msg.Seq)
		}
		for _, seq := range msg.OriginalSeq {
			if seq == 0 {
				return nil, fmt.Errorf("compactor: message %d has zero original sequence", msg.Seq)
			}
			originalSet[seq] = struct{}{}
		}
	}
	originalSeq := make([]uint64, 0, len(originalSet))
	for seq := range originalSet {
		originalSeq = append(originalSeq, seq)
	}
	sort.Slice(originalSeq, func(i, j int) bool { return originalSeq[i] < originalSeq[j] })
	if len(originalSeq) == 0 || originalSeq[0] != sources[0].Seq {
		return nil, fmt.Errorf("compactor: summary sequence %d does not match sources %v", sources[0].Seq, originalSeq)
	}

	summary := sharedkernel.Message{
		Seq:         sources[0].Seq,
		OriginalSeq: originalSeq,
		Role:        sharedkernel.RoleUser,
		Content:     content,
	}
	out := make([]sharedkernel.Message, 0, len(msgs)-(protectedStart-1)+1)
	out = append(out, msgs[0].Clone(), summary)
	out = append(out, sharedkernel.CloneMessages(msgs[protectedStart:])...)
	return out, nil
}
