package env

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

var WorkDir string
var MaxWinToken int = 1000 * 1000
var IsPlanMode bool

// Debug enables verbose logs, loaded from config.json at startup.
var Debug bool

// Config mirrors config.json at the workspace root.
type Config struct {
	Debug bool `json:"debug"`
}

// LoadConfig reads <workDir>/config.json into package vars.
// Missing file keeps defaults; unreadable or malformed file warns and continues.
func LoadConfig(workDir string) {
	data, err := os.ReadFile(filepath.Join(workDir, "config.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Printf("\033[33m[LaxCode] read config.json failed: %v\033[0m\n", err)
		}
		return
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		fmt.Printf("\033[33m[LaxCode] parse config.json failed: %v\033[0m\n", err)
		return
	}
	Debug = c.Debug
}

// Debugf prints only when Debug is on.
func Debugf(format string, args ...any) {
	if Debug {
		fmt.Printf("\033[90m[debug] "+format+"\033[0m\n", args...)
	}
}
