package tools

import "github.com/mikellxy/laxcode/internal/schema"

// 工具名常量：所有工具的定义与识别统一引用本组常量，
// 禁止在业务代码中散落工具名字符串字面量。
const (
	ToolBash        = "bash"
	ToolReadFile    = "read_file"
	ToolWriteFile   = "write_file"
	ToolEditFile    = "edit_file"
	ToolRunSubAgent = "run_sub_agent"
)

// AllReadFile 判断一组工具调用是否全部为 read_file。
// 引擎据此决定该批调用可否并行执行：read_file 只读且互不依赖，
// 并发读取不会产生写冲突，是安全的 fork-join 候选。
func AllReadFile(calls []schema.ToolCall) bool {
	for i := range calls {
		if calls[i].Name != ToolReadFile {
			return false
		}
	}
	return true
}
