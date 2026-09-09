// Package workfs 实现 domain/tools.WorkFS：文件类工具（read/write/edit）
// 唯一的 os 触点。领域层只持有端口，故沙箱内文件的真实读写、父目录创建与
// 权限位全部收口于此，os 错误原样透传（已满足 errors.Is(err, fs.ErrNotExist)）。
//
// 安全边界：本包不做路径校验。传入的 absPath 必须已由领域层的
// safeJoinWorkDir 解析并确认位于工作目录沙箱内——越界与绝对路径逃逸在那里
// 就被拒绝，此处不重复校验以免两处规则漂移。端口断言由组合同时导入两包的
// 组合根（cmd/agentasm）承担。
package workfs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// 沙箱内文件的权限位：目录 0o755、文件 0o644，与 umask 共同决定最终权限。
const (
	dirPerm  = 0o755
	filePerm = 0o644
)

// FS 是基于本地文件系统的 WorkFS 实现，无状态、可并发使用。
type FS struct{}

// New 返回本地文件系统实现。
func New() *FS { return &FS{} }

// OpenRead 打开 absPath 供顺序读取，调用方负责 Close。
func (FS) OpenRead(absPath string) (io.ReadCloser, error) {
	return os.Open(absPath)
}

// ReadFile 一次性读取 absPath 的全部内容。
func (FS) ReadFile(absPath string) ([]byte, error) {
	return os.ReadFile(absPath)
}

// WriteFile 写入 absPath，父目录不存在时自动创建。
func (FS) WriteFile(absPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(absPath), dirPerm); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	return os.WriteFile(absPath, content, filePerm)
}
