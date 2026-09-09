package tools

// ReadPaged 的算法测试：全部基于 strings.Reader，不触碰文件系统。
// 真实磁盘读取（打开失败、fixture 文件）的覆盖在 infrastructure/workfs。

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// readPagedStr 用内存 Reader 跑一次分页读取。
func readPagedStr(content string, nMax, linesMax, startLineNo, startBytes int) *PagedReadResult {
	return ReadPaged(strings.NewReader(content), PagedReadRequest{
		MaxBytes:    nMax,
		MaxLines:    linesMax,
		StartLineNo: startLineNo,
		StartBytes:  startBytes,
	})
}

// errReader 在读满 n 字节后返回非 EOF 错误，用于覆盖读取中途失败的分支。
type errReader struct {
	remain string
	err    error
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.remain == "" {
		return 0, r.err
	}
	n := copy(p, r.remain)
	r.remain = r.remain[n:]
	return n, nil
}

func TestReadPaged(t *testing.T) {
	// 4 行 11 字符、末行无换行符
	const fourLines = "11111111111\n22222222222\n33333333333\n44444444444"

	tests := []struct {
		name           string
		content        string
		nMax           int
		linesMax       int
		startLineNo    int
		startBytes     int
		wantContent    string
		wantLinesRead  int
		wantEndLineNo  int
		wantFinished   bool
		wantTruncated  bool
		wantTruncBytes int
	}{
		{
			name:          "全量读取含无换行符的末行",
			content:       fourLines,
			nMax:          10 * 1024,
			linesMax:      100,
			startLineNo:   1,
			wantContent:   "11111111111\n22222222222\n33333333333\n44444444444\n",
			wantLinesRead: 4,
			wantEndLineNo: 4,
			wantFinished:  true,
		},
		{
			name:          "行内字节偏移 startBytes=3 跳过前两字节",
			content:       fourLines,
			nMax:          10 * 1024,
			linesMax:      1,
			startLineNo:   1,
			startBytes:    3,
			wantContent:   "111111111\n",
			wantLinesRead: 1,
			wantEndLineNo: 1,
		},
		{
			name:          "startLineNo=2 从第二行读起",
			content:       fourLines,
			nMax:          10 * 1024,
			linesMax:      100,
			startLineNo:   2,
			wantContent:   "22222222222\n33333333333\n44444444444\n",
			wantLinesRead: 3,
			wantEndLineNo: 4,
			wantFinished:  true,
		},
		{
			name:          "linesMax 是本次读取行数上限而非结束行号",
			content:       fourLines,
			nMax:          10 * 1024,
			linesMax:      2,
			startLineNo:   2,
			wantContent:   "22222222222\n33333333333\n",
			wantLinesRead: 2,
			wantEndLineNo: 3,
		},
		{
			name:          "跳过前序行后仍可读满 linesMax 行",
			content:       fourLines,
			nMax:          10 * 1024,
			linesMax:      1,
			startLineNo:   2,
			wantContent:   "22222222222\n",
			wantLinesRead: 1,
			wantEndLineNo: 2,
		},
		{
			name:          "空行保留并计入行数",
			content:       "aaa\n\n\nbbb\n",
			nMax:          10 * 1024,
			linesMax:      10,
			startLineNo:   1,
			wantContent:   "aaa\n\n\nbbb\n",
			wantLinesRead: 4,
			wantEndLineNo: 4,
			wantFinished:  true,
		},
		{
			name:          "超长行跨缓冲分段完整读入",
			content:       strings.Repeat("X", 5000) + "\ntail\n",
			nMax:          20 * 1024,
			linesMax:      10,
			startLineNo:   1,
			wantContent:   strings.Repeat("X", 5000) + "\ntail\n",
			wantLinesRead: 2,
			wantEndLineNo: 2,
			wantFinished:  true,
		},
		{
			name:          "跳过超长行读取后续行",
			content:       strings.Repeat("X", 5000) + "\ntail\n",
			nMax:          20 * 1024,
			linesMax:      10,
			startLineNo:   2,
			wantContent:   "tail\n",
			wantLinesRead: 1,
			wantEndLineNo: 2,
			wantFinished:  true,
		},
		{
			name:          "长度恰为缓冲区整数倍的行不丢行尾换行",
			content:       "head\n" + strings.Repeat("Y", 8192) + "\n",
			nMax:          20 * 1024,
			linesMax:      10,
			startLineNo:   1,
			wantContent:   "head\n" + strings.Repeat("Y", 8192) + "\n",
			wantLinesRead: 2,
			wantEndLineNo: 2,
			wantFinished:  true,
		},
		{
			name:           "nMax 截断行内容时不补换行符",
			content:        "aaaa\nbbbb\n",
			nMax:           6,
			linesMax:       10,
			startLineNo:    1,
			wantContent:    "aaaa\nbb",
			wantLinesRead:  1,
			wantEndLineNo:  2,
			wantTruncated:  true,
			wantTruncBytes: 2,
		},
		{
			name:          "截断后按偏移续读补齐该行",
			content:       "aaaa\nbbbb\n",
			nMax:          6,
			linesMax:      10,
			startLineNo:   2,
			startBytes:    3,
			wantContent:   "bb\n",
			wantLinesRead: 1,
			wantEndLineNo: 2,
			wantFinished:  true,
		},
		{
			name:           "超长行中段截断记录跨段偏移",
			content:        strings.Repeat("X", 5000) + "\n",
			nMax:           4500,
			linesMax:       10,
			startLineNo:    1,
			wantContent:    strings.Repeat("X", 4500),
			wantLinesRead:  0,
			wantEndLineNo:  1,
			wantTruncated:  true,
			wantTruncBytes: 4500,
		},
		{
			name:          "超长行中段截断后续读（跨缓冲 skip）",
			content:       strings.Repeat("X", 5000) + "\n",
			nMax:          4500,
			linesMax:      10,
			startLineNo:   1,
			startBytes:    4501,
			wantContent:   strings.Repeat("X", 500) + "\n",
			wantLinesRead: 1,
			wantEndLineNo: 1,
			wantFinished:  true,
		},
		{
			name:           "续读再次截断时偏移须包含已跳过字节",
			content:        strings.Repeat("A", 4096) + strings.Repeat("B", 4096) + "\n",
			nMax:           4096,
			linesMax:       10,
			startLineNo:    1,
			startBytes:     4093,
			wantContent:    "AAAA" + strings.Repeat("B", 4092),
			wantLinesRead:  0,
			wantEndLineNo:  1,
			wantTruncated:  true,
			wantTruncBytes: 8188,
		},
		{
			name:          "按含跳过字节的偏移二次续读到行尾",
			content:       strings.Repeat("A", 4096) + strings.Repeat("B", 4096) + "\n",
			nMax:          4096,
			linesMax:      10,
			startLineNo:   1,
			startBytes:    8189,
			wantContent:   "BBBB\n",
			wantLinesRead: 1,
			wantEndLineNo: 1,
			wantFinished:  true,
		},
		{
			name:          "CRLF 归一为 LF",
			content:       "aaa\r\nbb\r\n\r\nccc\r\n",
			nMax:          10 * 1024,
			linesMax:      10,
			startLineNo:   1,
			wantContent:   "aaa\nbb\n\nccc\n",
			wantLinesRead: 4,
			wantEndLineNo: 4,
			wantFinished:  true,
		},
		{
			name:          "nMax 恰好在行边界用尽",
			content:       "abcd\nefgh\n",
			nMax:          4,
			linesMax:      10,
			startLineNo:   1,
			wantContent:   "abcd\n",
			wantLinesRead: 1,
			wantEndLineNo: 1,
		},
		{
			name:          "startBytes 超出行长时起始行按空行结束",
			content:       "abc\nxyz\n",
			nMax:          100,
			linesMax:      10,
			startLineNo:   1,
			startBytes:    100,
			wantContent:   "\nxyz\n",
			wantLinesRead: 2,
			wantEndLineNo: 2,
			wantFinished:  true,
		},
		{
			name:          "startLineNo 超出文件行数",
			content:       "a\nb\n",
			nMax:          100,
			linesMax:      10,
			startLineNo:   100,
			wantContent:   "",
			wantLinesRead: 0,
			wantEndLineNo: 2,
			wantFinished:  true,
		},
		{
			name:          "空内容",
			content:       "",
			nMax:          100,
			linesMax:      10,
			startLineNo:   1,
			wantContent:   "",
			wantLinesRead: 0,
			wantEndLineNo: 0,
			wantFinished:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := readPagedStr(tt.content, tt.nMax, tt.linesMax, tt.startLineNo, tt.startBytes)
			if res.Err != nil {
				t.Fatalf("unexpected err: %v", res.Err)
			}
			if string(res.Content) != tt.wantContent {
				t.Errorf("Content 不符:\n got  %q\n want %q", res.Content, tt.wantContent)
			}
			if res.LinesRead != tt.wantLinesRead {
				t.Errorf("LinesRead = %d, want %d", res.LinesRead, tt.wantLinesRead)
			}
			if res.EndLineNo != tt.wantEndLineNo {
				t.Errorf("EndLineNo = %d, want %d", res.EndLineNo, tt.wantEndLineNo)
			}
			if res.Finished != tt.wantFinished {
				t.Errorf("Finished = %v, want %v", res.Finished, tt.wantFinished)
			}
			if res.LastLineTruncated != tt.wantTruncated {
				t.Errorf("LastLineTruncated = %v, want %v", res.LastLineTruncated, tt.wantTruncated)
			}
			if res.LastLineTruncatedBytes != tt.wantTruncBytes {
				t.Errorf("LastLineTruncatedBytes = %d, want %d", res.LastLineTruncatedBytes, tt.wantTruncBytes)
			}
		})
	}
}

// TestReadPagedReadError 覆盖读取中途失败（非 EOF）的分支：错误原样透出，
// 且不置 Finished，避免调用方误判为文件已读完。
func TestReadPagedReadError(t *testing.T) {
	boom := errors.New("boom")
	res := ReadPaged(&errReader{remain: "partial\n", err: boom}, PagedReadRequest{
		MaxBytes: 1024, MaxLines: 10, StartLineNo: 1, StartBytes: 1,
	})
	if !errors.Is(res.Err, boom) {
		t.Fatalf("Err = %v, want %v", res.Err, boom)
	}
	if res.Finished {
		t.Error("读取失败时 Finished 应为 false")
	}
}

// TestReadPagedStartClamp 覆盖非法起始参数被钳制到行首（1-based）。
func TestReadPagedStartClamp(t *testing.T) {
	res := readPagedStr("aaa\nbbb\n", 1024, 10, 0, 0)
	if res.StartLineNo != 1 {
		t.Errorf("StartLineNo = %d, want 1", res.StartLineNo)
	}
	if got, want := string(res.Content), "aaa\nbbb\n"; got != want {
		t.Errorf("Content = %q, want %q", got, want)
	}
}

// TestReadPagedPaginateAll 端到端验证分页续读约定：
//   - 未截断：续读传 StartLineNo=EndLineNo+1、StartBytes=1
//   - 行被截断：续读传 StartLineNo=EndLineNo、StartBytes=LastLineTruncatedBytes+1
//
// 任意分页参数下拼接结果必须与一次性全量读取一致。
func TestReadPagedPaginateAll(t *testing.T) {
	contents := []struct {
		name    string
		content string
	}{
		{"末行无换行", "11111111111\n22222222222\n33333333333\n44444444444"},
		{"含空行", "aaa\n\n\nbbb\n\n"},
		{"纯空行文件", "\n\n\n"},
		{"超长行", strings.Repeat("X", 5000) + "\ntail\n" + strings.Repeat("Y", 8192) + "\nend\n"},
		{"CRLF", "aaa\r\nbb\r\n\r\nccc"},
		{"空内容", ""},
	}
	paramSets := [][2]int{
		{1, 1}, {2, 3}, {3, 2}, {5, 2}, {7, 100}, {100, 1}, {12, 4},
		{4096, 1}, {4096, 3}, {4500, 10}, {8192, 2},
	}

	for _, f := range contents {
		full := readPagedStr(f.content, 1<<20, 1<<20, 1, 1)
		if full.Err != nil {
			t.Fatalf("[%s] full read: %v", f.name, full.Err)
		}
		if !full.Finished {
			t.Fatalf("[%s] full read should finish", f.name)
		}

		for _, p := range paramSets {
			t.Run(fmt.Sprintf("%s/nMax=%d,linesMax=%d", f.name, p[0], p[1]), func(t *testing.T) {
				// 每页至少推进 1 字节或完整消费 1 行，步数上限按内容规模估算
				maxSteps := len(f.content) + strings.Count(f.content, "\n") + 128
				var buf bytes.Buffer
				startLine, startBytes := 1, 1
				finished := false
				for step := 0; step < maxSteps; step++ {
					res := readPagedStr(f.content, p[0], p[1], startLine, startBytes)
					if res.Err != nil {
						t.Fatalf("step %d: %v", step, res.Err)
					}
					buf.Write(res.Content)
					if res.Finished {
						finished = true
						break
					}
					if res.LastLineTruncated {
						startLine = res.EndLineNo
						startBytes = res.LastLineTruncatedBytes + 1
					} else {
						startLine = res.EndLineNo + 1
						startBytes = 1
					}
				}
				if !finished {
					t.Fatalf("分页遍历超过 %d 步未结束，疑似进度停滞", maxSteps)
				}
				if got, want := buf.String(), string(full.Content); got != want {
					t.Errorf("分页拼接与全量读取不一致:\n got  %q\n want %q", got, want)
				}
			})
		}
	}
}

// 编译期确保 errReader 满足 io.Reader。
var _ io.Reader = (*errReader)(nil)
