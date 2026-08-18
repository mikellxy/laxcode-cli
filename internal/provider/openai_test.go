package provider

import (
	"context"
	"testing"

	"github.com/mikellxy/laxcode/internal/schema"
)

func TestOpenAIProvider_Generate(t *testing.T) {
	t.Parallel()

	testSet := []struct {
		name string
		msgs []schema.Message
	}{
		{
			name: "system and user messages return assistant reply",
			msgs: []schema.Message{
				{Role: schema.RoleSystem, Content: "你是一个专业的AI编程助手"},
				{Role: schema.RoleUser, Content: "50字内介绍go语言的优势"},
			},
		},
	}

	p := NewOpenApiProvider()
	for _, test := range testSet {
		t.Run(test.name, func(t *testing.T) {
			assistantMsg, err := p.Generate(context.Background(), test.msgs)
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if assistantMsg.Role != schema.RoleAssistant {
				t.Errorf("Generate() role = %q, want %q", assistantMsg.Role, schema.RoleAssistant)
			}
			t.Logf("[%s] PASS. Generate() response = %q\n", test.name, assistantMsg.Content)
		})
	}
}
