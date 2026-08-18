package provider

import (
	"context"

	"github.com/mikellxy/laxcode/internal/schema"
)

type Provider interface {
	Generate(ctx context.Context, msgs []schema.Message) (*schema.Message, error)
}
