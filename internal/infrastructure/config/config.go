package config

import (
	"errors"
	"flag"
	"os"
	"path"
	"strings"

	"github.com/spf13/viper"
)

type envAndFileConf struct {
	OpenaiApiKey  string `mapstructure:"openai_api_key"`
	OpenaiBaseUrl string `mapstructure:"openai_base_url"`
	OpenaiModel   string `mapstructure:"openai_model"`
}

var EnvAndFileConf envAndFileConf

var EnvOrFile = viper.New()

type cliConf struct {
	Oneshot  bool   `mapstructure:"oneshot"`
	WorkDir  string `mapstructure:"workdir"`
	Task     string `mapstructure:"task"`
	TaskFile string `mapstructure:"task-file"`
	Session  string `mapstructure:"session"`
	Plan     bool   `mapstructure:"plan"`
}

var CliConf cliConf

var Cli = viper.New()

func ParseEnvAndFile() error {
	var filePath string
	homeDir, err := os.UserHomeDir()
	if err == nil {
		filePath = path.Join(homeDir, ".laxcode", "settings.json")
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

	EnvOrFile.BindEnv("openai_api_key", "OPENAI_API_KEY")
	EnvOrFile.BindEnv("openai_base_url", "OPENAI_BASE_URL")
	EnvOrFile.BindEnv("openai_model", "OPENAI_MODEL")
	EnvOrFile.SetEnvKeyReplacer(strings.NewReplacer("_", "_"))

	if err = EnvOrFile.Unmarshal(&EnvAndFileConf); err != nil {
		return err
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
	workDir := flag.String("workdir", "", "working directory; required in one-shot mode, defaults to cwd otherwise")
	task := flag.String("task", "", "one-shot task prompt text")
	taskFile := flag.String("task-file", "", "one-shot task prompt file path; takes precedence over -task")
	session := flag.String("session", "", "session id to resume; empty starts a new session")
	plan := flag.Bool("plan", false, "enable plan mode")
	flag.Parse()

	Cli.Set("oneshot", *oneshot)
	Cli.Set("workdir", *workDir)
	Cli.Set("task", *task)
	Cli.Set("task-file", *taskFile)
	Cli.Set("session", *session)
	Cli.Set("plan", *plan)

	return Cli.Unmarshal(&CliConf)
}
