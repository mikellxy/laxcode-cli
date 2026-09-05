package config

import (
	"errors"
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
	WorkDir  string `mapstructure:"work_dir"`
	Task     string `mapstructure:"task"`
	TaskFile string `mapstructure:"task_file"`
	Session  string `mapstructure:"session"`
	Plan     bool   `mapstructure:"plan"`
}

var CliConf cliConf

var Cli = viper.New()

func init() {
	if err := ParseEnvAndFile(); err != nil {
		panic(err)
	}
}

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
		if errors.As(err, &viper.ConfigFileNotFoundError{}) || errors.As(err, &os.ErrNotExist) {
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
