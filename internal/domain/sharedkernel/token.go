package sharedkernel

type TokenStatistics struct {
	TokenInput  int `json:"token_input"`
	TokenOutput int `json:"token_output"`
}

func (s *TokenStatistics) Add(other TokenStatistics) {
	s.TokenInput += other.TokenInput
	s.TokenOutput += other.TokenOutput
}

func (s *TokenStatistics) Minus(other TokenStatistics) {
	s.TokenInput -= other.TokenInput
	s.TokenOutput -= other.TokenOutput
}

func (s *TokenStatistics) OverWrite(other TokenStatistics) {
	s.TokenInput = other.TokenInput
	s.TokenOutput = other.TokenOutput
}

func (s *TokenStatistics) Total() int {
	return s.TokenInput + s.TokenOutput
}
