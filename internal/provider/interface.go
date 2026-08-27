package provider

import (
	"context"

	"github.com/mikellxy/laxcode/internal/schema"
)

type Provider interface {
	Generate(ctx context.Context, msgs []schema.Message, tools []schema.ToolDefinition) (*schema.Message, error)
	Info() *Info
}

type Info struct {
	Name string
}
