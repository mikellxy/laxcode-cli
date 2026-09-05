package sharedkernel

type SessionMeta struct {
	TokenUsed   TokenStatistics `json:"token_used"`
	WindowToken TokenStatistics `json:"window_token"`
}
