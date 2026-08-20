package engine

import "fmt"

var sysPrompt string = `你是一个智能AI助理，你可以处理的文件位于工作目录: %s
进行编程类任务时，如果无法确定文件，可以先试用 bash 工具，执行grep命令搜索目标代码在什么文件文件，然后使用 read 工具阅读代码，输出技术方案
`

func BuildSysPrompt(workDir string) string {
	return fmt.Sprintf(sysPrompt, workDir)
}
