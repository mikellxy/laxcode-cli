package tools

// read_file 的分页读取算法：只在 io.Reader 上做行号/字节偏移的记账，
// 不触碰文件系统，因此可脱离磁盘单测。文件句柄由 WorkFS 端口提供。

import (
	"bufio"
	"io"
)

// PagedReadRequest 是一次分页读取的入参。
// 续读约定（与 PagedReadResult 的字段配合）：
//   - 未截断：传 StartLineNo=EndLineNo+1、StartBytes=1
//   - 行被截断（LastLineTruncated=true）：传 StartLineNo=EndLineNo、
//     StartBytes=LastLineTruncatedBytes+1
type PagedReadRequest struct {
	// MaxBytes 是本次读取的内容字节上限，须为正。
	MaxBytes int
	// MaxLines 是本次读取的行数上限，须为正。
	MaxLines int
	// StartLineNo 是起始行号，1-based；小于 1 时按 1 处理。
	StartLineNo int
	// StartBytes 是起始行内的字节偏移，1-based；小于 1 时按 1 处理。
	StartBytes int
}

// PagedReadResult 是一次分页读取的结果。
type PagedReadResult struct {
	Content                []byte
	LinesRead              int  // 本次读到的完整行数（含行尾 \n）；被截断的末行不计入
	StartLineNo            int  // 实际生效的起始行号（入参被钳制后的值）
	EndLineNo              int  // 本次触及的最后一行行号
	LastLineTruncated      bool // 末行是否未读完整；其行尾 \n 尚未随 Content 返回时同样为 true
	LastLineTruncatedBytes int  // 末行已消费的字节偏移（不含换行符），续读传 +1
	Finished               bool // 是否已到达 EOF
	Err                    error
}

// ReadPaged 从 r 的 StartLineNo 行（1-based）、该行内 StartBytes 字节（1-based）
// 起读取，内容至多 MaxBytes 字节、至多 MaxLines 行。
// Content 中每行均以 \n 结尾（含文件本身无尾换行的末行）；\r\n 归一为 \n；
// 超出 bufio 缓冲区长度的行分段读取后拼接。
//
// MaxBytes 是 Content 的硬上限，行尾 \n 同样计入：若预算恰好在行内容末尾用尽，
// 该行按“未读完整”返回（LastLineTruncated=true、LastLineTruncatedBytes=行内容长度），
// 其行尾 \n 留到下一次调用返回，既不超预算也不丢内容。
func ReadPaged(r io.Reader, req PagedReadRequest) *PagedReadResult {
	// 行号与行内字节偏移均为 1-based，非法值钳制到行首
	startLineNo := req.StartLineNo
	if startLineNo < 1 {
		startLineNo = 1
	}
	startBytes := req.StartBytes
	if startBytes < 1 {
		startBytes = 1
	}

	result := &PagedReadResult{
		StartLineNo: startLineNo,
		Content:     make([]byte, 0, req.MaxBytes),
	}

	fr := bufio.NewReader(r)

	var (
		curLineNo            int
		lineOpen             bool
		linesRead            int
		bytesSkipInStartLine int
		nRead                int
		lineConsumed         int
	)

	for {
		if nRead >= req.MaxBytes || linesRead >= req.MaxLines {
			break
		}

		line, isPrefix, err := fr.ReadLine()
		if err != nil {
			if err == io.EOF {
				result.Finished = true
				break
			}
			result.Err = err
			return result
		}

		// 行号在行首自增：超长行的分段（isPrefix=true）只在行尾结束，若在
		// 分段处计数，首段会继承上一行的行号，进而在 startLineNo 之前被跳过
		if !lineOpen {
			curLineNo++
			lineOpen = true
			lineConsumed = 0
		}
		if !isPrefix {
			lineOpen = false
		}

		// 跳过不需要的行
		if curLineNo < startLineNo {
			continue
		}

		// 起始行内跳过不需要的字节
		if curLineNo == startLineNo && bytesSkipInStartLine < startBytes-1 {
			trySkip := min(len(line), startBytes-1-bytesSkipInStartLine)
			line = line[trySkip:]
			bytesSkipInStartLine += trySkip
			lineConsumed += trySkip
		}

		// 至多保留 MaxBytes 字节
		nKeep := min(len(line), req.MaxBytes-nRead)
		result.Content = append(result.Content, line[:nKeep]...)
		nRead += nKeep
		lineConsumed += nKeep

		// 行尾 \n 也占用字节预算：只有在整行内容都读入、且还能容纳 1 个 \n 时，
		// 才把这一行记为完整行并补 \n；否则本行按“未读完整”处理，\n 留给下一次
		// 调用（start_bytes=LastLineTruncatedBytes+1）返回。
		// 这样 Content 长度恒等于 nRead，绝不会超出 MaxBytes，且不丢字节。
		if !isPrefix && nKeep == len(line) && nRead < req.MaxBytes {
			result.Content = append(result.Content, byte('\n'))
			nRead++
			linesRead++
			result.LastLineTruncated = false
			result.LastLineTruncatedBytes = 0
		} else {
			result.LastLineTruncated = true
			result.LastLineTruncatedBytes = lineConsumed
		}
	}

	result.LinesRead = linesRead
	result.EndLineNo = curLineNo
	return result
}
