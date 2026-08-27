package env

import (
	"fmt"
)

var MaxWinToken int = 1000 * 1000

// Debug enables verbose logs, loaded from config.json at startup.
var Debug bool

// Config mirrors config.json at the workspace root.
type Config struct {
	Debug bool `json:"debug"`
}

// LoadConfig reads <workDir>/config.json into package vars.
// Missing file keeps defaults; unreadable or malformed file warns and continues.
func LoadConfig(deBug bool) error {
	Debug = deBug
	return nil
}

// Debugf prints only when Debug is on.
func Debugf(format string, args ...any) {
	if Debug {
		fmt.Printf("\033[90m[debug] "+format+"\033[0m\n", args...)
	}
}
