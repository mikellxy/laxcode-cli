// Package config 按"命令行参数 > 环境变量 > ~/.laxcode/settings.json"的
// 优先级解析配置项。配置项通过 String/Bool 声明（声明即注册对应命令行
// 参数），main 在声明完全部配置项后调用 Parse 一次，之后各处用 Get 取值。
package config

import (
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mikellxy/laxcode/internal/printer"
)

// Item 描述一个配置项及其支持的来源；Flag、Env、Key 留空表示禁用对应来源。
// 配置项名称约定全大写；命令行参数对外为小写，注册时由 Flag 自动转小写，
// 环境变量名和 settings.json 键保持大写原名。
type Item struct {
	Flag  string // 配置项名称，命令行参数为其小写形式
	Env   string // 环境变量名
	Key   string // ~/.laxcode/settings.json 中的键
	Usage string // 命令行参数的帮助文本
}

var (
	// explicitFlags 记录命令行显式传入的参数名，用于区分"未传"与"传了零值"，
	// 只有显式传入的参数才参与最高优先级解析
	explicitFlags = map[string]bool{}
	settings      = map[string]any{}
)

// Parse 解析命令行参数并加载 ~/.laxcode/settings.json，必须在所有配置项
// 声明之后、任何 Get 之前调用。settings.json 不存在不视为错误。
func Parse() error {
	flag.Parse()
	flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(home, ".laxcode", "settings.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &settings)
}

// StringValue 是一个字符串配置项。
type StringValue struct {
	item Item
	flag *string
}

// String 声明一个字符串配置项；item.Flag 非空时注册命令行参数（小写形式）。
func String(item Item) *StringValue {
	v := &StringValue{item: item}
	if item.Flag != "" {
		v.flag = flag.String(flagName(item.Flag), "", item.Usage)
	}
	return v
}

// flagName 返回配置项对外暴露的小写命令行参数名。
func flagName(name string) string {
	return strings.ToLower(name)
}

// Get 按 命令行参数 > 环境变量 > settings.json 的优先级取值。
func (v *StringValue) Get() string {
	if v.flag != nil && explicitFlags[flagName(v.item.Flag)] {
		return *v.flag
	}
	if v.item.Env != "" {
		if s := os.Getenv(v.item.Env); s != "" {
			return s
		}
	}
	if v.item.Key != "" {
		if s, ok := settings[v.item.Key].(string); ok {
			return s
		}
	}
	return ""
}

// BoolValue 是一个布尔配置项。
type BoolValue struct {
	item Item
	flag *bool
}

// Bool 声明一个布尔配置项；item.Flag 非空时注册命令行参数（小写形式）。
func Bool(item Item) *BoolValue {
	v := &BoolValue{item: item}
	if item.Flag != "" {
		v.flag = flag.Bool(flagName(item.Flag), false, item.Usage)
	}
	return v
}

// Get 按 命令行参数 > 环境变量 > settings.json 的优先级取值；
// 环境变量按 strconv.ParseBool 解析，非法值落到下一来源。
func (v *BoolValue) Get() bool {
	if v.flag != nil && explicitFlags[flagName(v.item.Flag)] {
		return *v.flag
	}
	if v.item.Env != "" {
		if s := os.Getenv(v.item.Env); s != "" {
			if b, err := strconv.ParseBool(s); err == nil {
				return b
			}
		}
	}
	if v.item.Key != "" {
		if b, ok := settings[v.item.Key].(bool); ok {
			return b
		}
	}
	return false
}

// Debug 是调试日志开关，只支持命令行 -debug。
var Debug = Bool(Item{Flag: "DEBUG", Usage: "enable debug mode"})

// Debugf 仅在 Debug 开启时打印（灰 [debug] 前缀经 printer 包级默认实例落笔）。
func Debugf(format string, args ...any) {
	if Debug.Get() {
		printer.Debugf(format, args...)
	}
}

// MaxWinToken 是触发上下文压缩的窗口 token 预算。
var MaxWinToken = 1000 * 1000
