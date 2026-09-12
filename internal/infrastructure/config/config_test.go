package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// swapConfigGlobals 隔离 ParseEnvAndFile 依赖的包级全局，测试结束恢复。
func swapConfigGlobals(t *testing.T) {
	t.Helper()
	prevViper := EnvOrFile
	prevConf := EnvAndFileConf
	EnvOrFile = viper.New()
	EnvAndFileConf = envAndFileConf{}
	t.Cleanup(func() {
		EnvOrFile = prevViper
		EnvAndFileConf = prevConf
	})
}

func writeSettings(t *testing.T, home string, content string) {
	t.Helper()
	dir := filepath.Join(home, ".laxcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func TestParseEnvAndFileFromEnv(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-env-key")
	t.Setenv("OPENAI_BASE_URL", "https://env.example.com/v1")
	t.Setenv("OPENAI_MODEL", "gpt-env")

	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if EnvAndFileConf.OpenaiApiKey != "sk-env-key" {
		t.Errorf("api key 应从环境读取，实际 %q", EnvAndFileConf.OpenaiApiKey)
	}
	if EnvAndFileConf.OpenaiBaseUrl != "https://env.example.com/v1" ||
		EnvAndFileConf.OpenaiModel != "gpt-env" {
		t.Errorf("base url/model 应从环境读取，实际 %+v", EnvAndFileConf)
	}
	if EnvAndFileConf.OpenaiContextWindow != DefaultContextWindow ||
		EnvAndFileConf.OpenaiMaxOutputTokens != DefaultMaxOutputTokens {
		t.Errorf("context budget defaults not applied: %+v", EnvAndFileConf)
	}
	if EnvAndFileConf.LlmRouterAddr != DefaultLLMRouterAddr {
		t.Errorf("llm router default addr = %q", EnvAndFileConf.LlmRouterAddr)
	}
}

func TestParseEnvAndFileLLMRouterAddrFromEnv(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LLM_ROUTER_ADDR", "127.0.0.1:18080")

	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if EnvAndFileConf.LlmRouterAddr != "127.0.0.1:18080" {
		t.Fatalf("llm router addr = %q", EnvAndFileConf.LlmRouterAddr)
	}
}

func TestParseEnvAndFileEnvOverridesFile(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"openai_api_key":"sk-file-key","openai_base_url":"https://file.example.com/v1","openai_model":"gpt-file"}`)

	// 环境变量优先于 settings.json；base url 刻意置空（viper 忽略空环境变量），
	// 以便验证未设置环境变量的项回落配置文件
	t.Setenv("OPENAI_API_KEY", "sk-env-key")
	t.Setenv("OPENAI_MODEL", "gpt-env")
	t.Setenv("OPENAI_BASE_URL", "")

	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if EnvAndFileConf.OpenaiApiKey != "sk-env-key" || EnvAndFileConf.OpenaiModel != "gpt-env" {
		t.Errorf("环境变量应覆盖文件配置，实际 %+v", EnvAndFileConf)
	}
	// 未设置环境变量的项保留文件值
	if EnvAndFileConf.OpenaiBaseUrl != "https://file.example.com/v1" {
		t.Errorf("base url 应回落到文件值，实际 %q", EnvAndFileConf.OpenaiBaseUrl)
	}
}

func TestParseEnvAndFileNoHomeNoError(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-1")
	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("无 settings.json 时不应报错：%v", err)
	}
	if EnvAndFileConf.OpenaiApiKey != "sk-1" {
		t.Errorf("无配置文件时应从环境读取，实际 %q", EnvAndFileConf.OpenaiApiKey)
	}
}

func TestParseEnvAndFileCorruptFileReturnsError(t *testing.T) {
	swapConfigGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeSettings(t, home, `{"openai_api_key": `) // 非法 JSON

	if err := ParseEnvAndFile(); err == nil {
		t.Fatal("settings.json 非法时应返回错误")
	}
}

func TestParseEnvAndFileContextBudgetFromEnv(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_CONTEXT_WINDOW", "1000000")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "32768")
	if err := ParseEnvAndFile(); err != nil {
		t.Fatalf("ParseEnvAndFile: %v", err)
	}
	if EnvAndFileConf.OpenaiContextWindow != 1_000_000 || EnvAndFileConf.OpenaiMaxOutputTokens != 32_768 {
		t.Fatalf("context budget env was not applied: %+v", EnvAndFileConf)
	}
}

func TestParseEnvAndFileCompactionProviderOverridesAndFallbacks(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "main-key")
	t.Setenv("OPENAI_BASE_URL", "https://main.example/v1")
	t.Setenv("OPENAI_MODEL", "main-model")
	t.Setenv("OPENAI_CONTEXT_WINDOW", "100000")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "10000")
	t.Setenv("COMPACTION_OPENAI_MODEL", "summary-model")
	t.Setenv("COMPACTION_OPENAI_MAX_OUTPUT_TOKENS", "2000")

	if err := ParseEnvAndFile(); err != nil {
		t.Fatal(err)
	}
	if EnvAndFileConf.CompactionOpenaiApiKey != "main-key" ||
		EnvAndFileConf.CompactionOpenaiBaseUrl != "https://main.example/v1" ||
		EnvAndFileConf.CompactionOpenaiModel != "summary-model" ||
		EnvAndFileConf.CompactionOpenaiContextWindow != 100000 ||
		EnvAndFileConf.CompactionOpenaiMaxOutputTokens != 2000 {
		t.Fatalf("unexpected compaction provider config: %+v", EnvAndFileConf)
	}
}

func TestParseEnvAndFileRejectsInvalidContextBudget(t *testing.T) {
	swapConfigGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_CONTEXT_WINDOW", "1000")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "1000")
	if err := ParseEnvAndFile(); err == nil {
		t.Fatal("max output equal to context window must fail")
	}
}

// swapCliGlobals 隔离 ParseCli 依赖的包级全局（flag 集合、os.Args、Cli viper）。
func swapCliGlobals(t *testing.T, args ...string) {
	t.Helper()
	prevArgs := os.Args
	prevFlagCmd := flag.CommandLine
	prevCli := Cli
	prevConf := CliConf

	flag.CommandLine = flag.NewFlagSet("config-test", flag.ContinueOnError)
	os.Args = append([]string{"config.test"}, args...)
	Cli = viper.New()
	CliConf = cliConf{}

	t.Cleanup(func() {
		os.Args = prevArgs
		flag.CommandLine = prevFlagCmd
		Cli = prevCli
		CliConf = prevConf
	})
}

func TestParseCli(t *testing.T) {
	swapCliGlobals(t,
		"-oneshot=true",
		"-sse=true",
		"-addr", ":9000",
		"-workdir", "/tmp/proj",
		"-task", "do something",
		"-task-file", "/tmp/t.txt",
		"-session", "sess-9",
		"-plan=true",
	)

	if err := ParseCli(); err != nil {
		t.Fatalf("ParseCli: %v", err)
	}
	if !CliConf.Oneshot {
		t.Error("oneshot 应为 true")
	}
	if CliConf.WorkDir != "/tmp/proj" || CliConf.Task != "do something" ||
		CliConf.TaskFile != "/tmp/t.txt" || CliConf.Session != "sess-9" {
		t.Errorf("字符串参数解析不符：%+v", CliConf)
	}
	if !CliConf.Plan {
		t.Error("plan 应为 true")
	}
	if !CliConf.SSE {
		t.Error("sse 应为 true")
	}
	if CliConf.Addr != ":9000" {
		t.Errorf("addr 应为 :9000，实际 %q", CliConf.Addr)
	}
}

func TestParseCliDefaults(t *testing.T) {
	swapCliGlobals(t)
	if err := ParseCli(); err != nil {
		t.Fatalf("ParseCli with no args: %v", err)
	}
	if CliConf.Oneshot || CliConf.Plan || CliConf.SSE {
		t.Errorf("缺省布尔参数应全为 false，实际 %+v", CliConf)
	}
	if CliConf.WorkDir != "" || CliConf.Task != "" || CliConf.Session != "" {
		t.Errorf("缺省字符串参数应为空，实际 %+v", CliConf)
	}
	if CliConf.Addr != DefaultSSEAddr {
		t.Errorf("缺省 addr 应为 %q，实际 %q", DefaultSSEAddr, CliConf.Addr)
	}
}
