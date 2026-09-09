package tools

// 文件工具的沙箱端口：领域层只定义契约，真正的 os 调用由基础设施实现
// （internal/infrastructure/workfs）。路径解析与越界校验属领域安全规则，
// 仍留在领域层（见 safeJoinWorkDir），端口只接受已解析好的绝对路径。

import "io"

// WorkFS 抽象工作目录沙箱内的文件读写。实现方须保证：文件不存在时返回的
// 错误满足 errors.Is(err, fs.ErrNotExist)，使领域层无需依赖 os 即可分类错误。
type WorkFS interface {
	// OpenRead 打开 absPath 供顺序读取，调用方负责 Close。
	OpenRead(absPath string) (io.ReadCloser, error)
	// ReadFile 一次性读取 absPath 的全部内容。
	ReadFile(absPath string) ([]byte, error)
	// WriteFile 写入 absPath，父目录不存在时自动创建。
	WriteFile(absPath string, content []byte) error
}
