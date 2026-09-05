package prompt

import (
	_ "embed"
)

//go:embed tmpl/personality.md
var personalityPrompt string

func GetSysPrompt() string {
	return personalityPrompt
}
