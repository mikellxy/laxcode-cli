package tools

import "fmt"

const (
	errTypeTool = "tool"
	errTypeOs   = "os"
)

type ErrorWithPrompt interface {
	Error() string
	Wrap(err error)
	AsPrompt() (string, bool)
}

var ErrPromptTmpl = `### error_type: %s
### error_detail: %s
### suggestion: %s`

func buildErrPrompt(errType, errDetail, sug string) string {
	return fmt.Sprintf(ErrPromptTmpl, errType, errDetail, sug)
}

func NewErrorWithPrompt(e ErrorWithPrompt, innerErr error) ErrorWithPrompt {
	e.Wrap(innerErr)
	return e
}

type errWithPromptBase struct {
	Err error
}

func (f *errWithPromptBase) Error() string {
	if f.Err == nil {
		return ""
	}
	return f.Err.Error()
}

func (f *errWithPromptBase) Wrap(err error) {
	f.Err = err
}

type ParamError struct {
	errWithPromptBase
}

func (f *ParamError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"工具调用参数错误或缺失，仔细阅读工具定义并修正",
	), true
}

type FilePathError struct {
	errWithPromptBase
}

func (f *FilePathError) AsPrompt() (string, bool) {
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"检查文件路径格式标准，是否逃逸出限制目录，并修正",
	), true
}

// FileNotExistError 目标文件不存在，suggestion 引导核对路径并提示
// 编辑仅限已有文件、新建文件应改用 write_file。
type FileNotExistError struct {
	errWithPromptBase
}

func (f *FileNotExistError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"核对文件路径是否为工作目录内正确的相对路径；编辑仅支持已存在的文件，新建文件请使用 write_file",
	), true
}

// EditNotFoundError edit_file 未找到匹配，suggestion 阻止模型凭记忆
// 盲目重试，强制其重新 read_file 获取最新内容后再编辑。
type EditNotFoundError struct {
	errWithPromptBase
}

func (f *EditNotFoundError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"禁止凭记忆盲目重试。文件内容可能已变化或 old_text 与文件不一致，请先用 read_file 重新获取文件最新内容，再逐字核对并重新组装 old_text 后重试",
	), true
}

// EditMultiMatchError edit_file 的 old_text 多处命中，suggestion 引导
// 扩大 old_text 上下文范围使匹配唯一。
type EditMultiMatchError struct {
	errWithPromptBase
}

func (f *EditMultiMatchError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeTool,
		f.Err.Error(),
		"old_text 在文件中匹配到多处，请扩大 old_text 范围纳入更多上下文行使其唯一后重试",
	), true
}

// FileIOError 文件读写类系统错误（权限、磁盘、占用等），error_type 为 os。
type FileIOError struct {
	errWithPromptBase
}

func (f *FileIOError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}
	return buildErrPrompt(errTypeOs,
		f.Err.Error(),
		"文件读写失败，可能是权限、磁盘或文件被占用等系统问题，请检查文件权限与状态，不要盲目重试相同操作",
	), true
}

type BashExecuteError struct {
	errWithPromptBase
}

// AsPrompt 生成给到LLM的纠错提示
func (f *BashExecuteError) AsPrompt() (string, bool) {
	if f.Err == nil {
		return "", false
	}

	return buildErrPrompt(
		errTypeTool,
		f.Err.Error(),
		`Bash进程执行发生系统故障。
排查方向：
1.命令执行超时，可拆分任务或者延长超时时间；
2.工作目录不存在、权限不足；
3.命令语法错误、可执行文件找不到；
不要反复原样重试，先检查命令与工作环境。`,
	), true
}
