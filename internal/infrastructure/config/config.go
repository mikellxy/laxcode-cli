package config

import (
	"errors"
	"flag"
	"os"
	"strings"

	"github.com/mikellxy/laxcode/internal/infrastructure/layout"
	"github.com/spf13/viper"
)

type envAndFileConf struct {
	OpenaiApiKey                    string `mapstructure:"openai_api_key"`
	OpenaiBaseUrl                   string `mapstructure:"openai_base_url"`
	OpenaiModel                     string `mapstructure:"openai_model"`
	OpenaiContextWindow             int    `mapstructure:"openai_context_window"`
	OpenaiMaxOutputTokens           int    `mapstructure:"openai_max_output_tokens"`
	CompactionOpenaiApiKey          string `mapstructure:"compaction_openai_api_key"`
	CompactionOpenaiBaseUrl         string `mapstructure:"compaction_openai_base_url"`
	CompactionOpenaiModel           string `mapstructure:"compaction_openai_model"`
	CompactionOpenaiContextWindow   int    `mapstructure:"compaction_openai_context_window"`
	CompactionOpenaiMaxOutputTokens int    `mapstructure:"compaction_openai_max_output_tokens"`
}

const (
	// 兼容端点的 /models 响应不会标准化暴露 context window，
	// 因此给出保守默认值，并允许按实际部署显式配置。
	DefaultContextWindow   = 200_000
	DefaultMaxOutputTokens = 4096
)

var EnvAndFileConf envAndFileConf

var EnvOrFile = viper.New()

type cliConf struct {
	Oneshot  bool   `mapstructure:"oneshot"`
	SSE      bool   `mapstructure:"sse"`
	Addr     string `mapstructure:"addr"`
	WorkDir  string `mapstructure:"workdir"`
	Task     string `mapstructure:"task"`
	TaskFile string `mapstructure:"task-file"`
	Session  string `mapstructure:"session"`
	Plan     bool   `mapstructure:"plan"`
}

// DefaultSSEAddr 是 sse server 模式的缺省监听地址：仅绑定本地回环，因为
// Agent 具备 bash / 写文件能力，默认不对外暴露；需要对外时以 -addr 覆盖。
const DefaultSSEAddr = "127.0.0.1:8080"

var CliConf cliConf

var Cli = viper.New()

func ParseEnvAndFile() error {
	var filePath string
	homeDir, err := os.UserHomeDir()
	if err == nil {
		// 用户级配置路径统一由布局包拼装（${home}/.laxcode/settings.json）
		filePath = layout.UserSettings(homeDir)
	}

	if filePath != "" {
		EnvOrFile.SetConfigFile(filePath)
	}
	err = EnvOrFile.ReadInConfig()
	if err != nil {
		if errors.As(err, &viper.ConfigFileNotFoundError{}) || errors.Is(err, os.ErrNotExist) {
		} else {
			return err
		}
	}

	EnvOrFile.SetDefault("openai_context_window", DefaultContextWindow)
	EnvOrFile.SetDefault("openai_max_output_tokens", DefaultMaxOutputTokens)
	EnvOrFile.BindEnv("openai_api_key", "OPENAI_API_KEY")
	EnvOrFile.BindEnv("openai_base_url", "OPENAI_BASE_URL")
	EnvOrFile.BindEnv("openai_model", "OPENAI_MODEL")
	EnvOrFile.BindEnv("openai_context_window", "OPENAI_CONTEXT_WINDOW")
	EnvOrFile.BindEnv("openai_max_output_tokens", "OPENAI_MAX_OUTPUT_TOKENS")
	EnvOrFile.BindEnv("compaction_openai_api_key", "COMPACTION_OPENAI_API_KEY")
	EnvOrFile.BindEnv("compaction_openai_base_url", "COMPACTION_OPENAI_BASE_URL")
	EnvOrFile.BindEnv("compaction_openai_model", "COMPACTION_OPENAI_MODEL")
	EnvOrFile.BindEnv("compaction_openai_context_window", "COMPACTION_OPENAI_CONTEXT_WINDOW")
	EnvOrFile.BindEnv("compaction_openai_max_output_tokens", "COMPACTION_OPENAI_MAX_OUTPUT_TOKENS")
	EnvOrFile.SetEnvKeyReplacer(strings.NewReplacer("_", "_"))

	if err = EnvOrFile.Unmarshal(&EnvAndFileConf); err != nil {
		return err
	}
	// 压缩 provider 默认继承主 provider；通常只需配置一个更便宜的模型。
	if EnvAndFileConf.CompactionOpenaiApiKey == "" {
		EnvAndFileConf.CompactionOpenaiApiKey = EnvAndFileConf.OpenaiApiKey
	}
	if EnvAndFileConf.CompactionOpenaiBaseUrl == "" {
		EnvAndFileConf.CompactionOpenaiBaseUrl = EnvAndFileConf.OpenaiBaseUrl
	}
	if EnvAndFileConf.CompactionOpenaiModel == "" {
		EnvAndFileConf.CompactionOpenaiModel = EnvAndFileConf.OpenaiModel
	}
	if EnvAndFileConf.CompactionOpenaiContextWindow == 0 {
		EnvAndFileConf.CompactionOpenaiContextWindow = EnvAndFileConf.OpenaiContextWindow
	}
	if EnvAndFileConf.CompactionOpenaiMaxOutputTokens == 0 {
		EnvAndFileConf.CompactionOpenaiMaxOutputTokens = EnvAndFileConf.OpenaiMaxOutputTokens
	}
	if EnvAndFileConf.OpenaiContextWindow <= 0 {
		return errors.New("openai_context_window must be positive")
	}
	if EnvAndFileConf.OpenaiMaxOutputTokens <= 0 ||
		EnvAndFileConf.OpenaiMaxOutputTokens >= EnvAndFileConf.OpenaiContextWindow {
		return errors.New("openai_max_output_tokens must be positive and smaller than openai_context_window")
	}
	if EnvAndFileConf.CompactionOpenaiContextWindow <= 0 {
		return errors.New("compaction_openai_context_window must be positive")
	}
	if EnvAndFileConf.CompactionOpenaiMaxOutputTokens <= 0 ||
		EnvAndFileConf.CompactionOpenaiMaxOutputTokens >= EnvAndFileConf.CompactionOpenaiContextWindow {
		return errors.New("compaction_openai_max_output_tokens must be positive and smaller than compaction_openai_context_window")
	}

	return nil
}

// ParseCli 解析命令行参数到 CliConf：用标准库 flag 定义与 cliConf 字段
// 一一对应的参数（flag 名与 mapstructure tag 保持一致，Cli viper 方能按
// key 匹配），flag.Parse 后把各值写入 Cli 实例再 Unmarshal 到 CliConf，
// 与 ParseEnvAndFile 的 viper 装配风格对称。
//
// 与 ParseEnvAndFile 不同，本函数内含 flag.Parse 会消费 os.Args，须由 main
// 在启动早期显式调用，不宜放入包 init——否则 go test 的测试二进制会在
// testing 注册 -test.* 参数之前执行 flag.Parse，遇到 -test.v 等以“未定义
// 参数”直接退出（老 internal/config 亦是由 main 显式调用 Parse）。
func ParseCli() error {
	oneshot := flag.Bool("oneshot", false, "one-shot mode: run a single task and print structured JSON to stdout")
	sse := flag.Bool("sse", false, "sse server mode: serve HTTP POST /chat and stream ReAct events over SSE")
	addr := flag.String("addr", DefaultSSEAddr, "sse server listen address")
	workDir := flag.String("workdir", "", "working directory; required in one-shot mode, defaults to cwd otherwise")
	task := flag.String("task", "", "one-shot task prompt text")
	taskFile := flag.String("task-file", "", "one-shot task prompt file path; takes precedence over -task")
	session := flag.String("session", "", "session id to resume; empty starts a new session")
	plan := flag.Bool("plan", false, "enable plan mode")
	flag.Parse()

	Cli.Set("oneshot", *oneshot)
	Cli.Set("sse", *sse)
	Cli.Set("addr", *addr)
	Cli.Set("workdir", *workDir)
	Cli.Set("task", *task)
	Cli.Set("task-file", *taskFile)
	Cli.Set("session", *session)
	Cli.Set("plan", *plan)

	return Cli.Unmarshal(&CliConf)
}
