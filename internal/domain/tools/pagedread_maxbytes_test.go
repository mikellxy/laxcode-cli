package tools

// ReadPaged 字节上限（MaxBytes 计入行尾换行符）的回归测试。
// 所有用例都要求 len(Content) <= MaxBytes，且按既有续读约定翻页后，
// 拼接结果与“规范化后的原文件”（\r\n 归一为 \n、无尾换行的末行补 \n）完全一致。

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// normalizedContent 独立生成 ReadPaged 约定的规范化结果，避免用被测函数
// 自身生成期望值而掩盖全量读取与分页读取共有的错误。
func normalizedContent(content string) string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if normalized != "" && !strings.HasSuffix(normalized, "\n") {
		normalized += "\n"
	}
	return normalized
}

// paginate 按既有续读约定翻页读完全文，逐页校验：
//   - len(Content) <= MaxBytes（行尾换行符同样受限）
//   - LinesRead 与 Content 中换行符个数一致（完整行都带行尾 \n，未截断行不丢换行符）
//   - 截断标记与截断偏移自洽（截断时偏移 >= 1，续读参数始终前进）
//   - 标了 Finished 的位置再续读必须为空且仍为 Finished（EOF 语义）
//
// 返回拼接结果与页数。
func paginate(t *testing.T, content string, nMax, linesMax int) (string, int) {
	t.Helper()

	var buf bytes.Buffer
	startLine, startBytes := 1, 1
	// 每页至少推进 1 字节，页数上限按规范化后的内容长度估算
	maxSteps := len(content) + strings.Count(content, "\n") + 8

	for step := 1; step <= maxSteps; step++ {
		res := readPagedStr(content, nMax, linesMax, startLine, startBytes)
		if res.Err != nil {
			t.Fatalf("step %d: %v", step, res.Err)
		}
		where := fmt.Sprintf("content=%q nMax=%d linesMax=%d step=%d(startLine=%d,startBytes=%d)",
			content, nMax, linesMax, step, startLine, startBytes)

		if len(res.Content) > nMax {
			t.Fatalf("%s: len(Content)=%d 超过 MaxBytes，Content=%q", where, len(res.Content), res.Content)
		}
		if got, want := res.LinesRead, bytes.Count(res.Content, []byte{'\n'}); got != want {
			t.Fatalf("%s: LinesRead=%d 与 Content 中换行符个数 %d 不一致，Content=%q", where, got, want, res.Content)
		}
		if res.LastLineTruncated {
			if res.LastLineTruncatedBytes < 1 {
				t.Fatalf("%s: 截断页的 LastLineTruncatedBytes=%d 无法推进续读", where, res.LastLineTruncatedBytes)
			}
		} else if res.LastLineTruncatedBytes != 0 {
			t.Fatalf("%s: 未截断页的 LastLineTruncatedBytes=%d 应为 0", where, res.LastLineTruncatedBytes)
		}

		buf.Write(res.Content)

		if res.Finished {
			// EOF 之后再按协议续读，不应再有任何内容
			if next := readPagedStr(content, nMax, linesMax, res.EndLineNo+1, 1); len(next.Content) != 0 || !next.Finished {
				t.Fatalf("%s: 已标 Finished 但仍可续读出 %q (Finished=%v)", where, next.Content, next.Finished)
			}
			return buf.String(), step
		}
		if res.LastLineTruncated {
			startLine, startBytes = res.EndLineNo, res.LastLineTruncatedBytes+1
		} else {
			startLine, startBytes = res.EndLineNo+1, 1
		}
	}

	t.Fatalf("content=%q nMax=%d linesMax=%d: 分页遍历超过 %d 步未结束", content, nMax, linesMax, maxSteps)
	return "", 0
}

// TestReadPagedMaxBytesBoundary 覆盖字节上限的各类边界：首屏字段须精确，
// 且按续读约定继续读取后拼接等于规范化后的原文件。
func TestReadPagedMaxBytesBoundary(t *testing.T) {
	// 超长行（跨 4096 缓冲）用：
	const longLine = 5000
	cases := []struct {
		name             string
		content          string
		nMax             int
		linesMax         int
		wantContent      string // 首屏内容
		wantLines        int    // 首屏 LinesRead
		wantEndLine      int    // 首屏 EndLineNo
		wantTrunc        bool   // 首屏 LastLineTruncated
		wantTruncN       int    // 首屏 LastLineTruncatedBytes
		wantFinished     bool   // 首屏 Finished
		wantNext         string // 按续读约定取到的第二屏内容
		wantNextLines    int
		wantNextEndLine  int
		wantNextTrunc    bool
		wantNextTruncN   int
		wantNextFinished bool
	}{
		{
			// 每行 1 字节 + 换行 = 2 字节；7 字节预算只够 3 整行 + 第 4 行内容
			name:        "多行短文本行尾换行符计入预算",
			content:     strings.Repeat("a\n", 10),
			nMax:        7,
			linesMax:    100,
			wantContent: "a\na\na\na",
			wantLines:   3,
			wantEndLine: 4,
			wantTrunc:   true,
			wantTruncN:  1,
			// 先补回第 4 行的 \n，再读 3 整行
			wantNext:        "\na\na\na\n",
			wantNextLines:   4,
			wantNextEndLine: 7,
		},
		{
			// 预算恰好在行内容末尾用尽，塞不下行尾 \n：换行符留待续读
			name:            "预算恰好落在行内容末尾",
			content:         "abcd\nefgh\n",
			nMax:            4,
			linesMax:        100,
			wantContent:     "abcd",
			wantLines:       0,
			wantEndLine:     1,
			wantTrunc:       true,
			wantTruncN:      4,
			wantNext:        "\nefg",
			wantNextLines:   1,
			wantNextEndLine: 2,
			wantNextTrunc:   true,
			wantNextTruncN:  3,
		},
		{
			// 预算恰好包含行尾 \n：该行算完整行，不标记截断
			name:            "预算恰好包含换行符",
			content:         "abcd\nefgh\n",
			nMax:            5,
			linesMax:        100,
			wantContent:     "abcd\n",
			wantLines:       1,
			wantEndLine:     1,
			wantNext:        "efgh\n",
			wantNextLines:   1,
			wantNextEndLine: 2,
		},
		{
			// 文件末行无尾换行，规范化后仍需补 \n；预算恰好等于行内容长度
			name:             "无尾换行的末行补 \n 不超预算",
			content:          "abcde",
			nMax:             5,
			linesMax:         100,
			wantContent:      "abcde",
			wantLines:        0,
			wantEndLine:      1,
			wantTrunc:        true,
			wantTruncN:       5,
			wantNext:         "\n",
			wantNextLines:    1,
			wantNextEndLine:  1,
			wantNextFinished: true,
		},
		{
			// 超长行在缓冲分段处被截断，续读从分段边界继续
			name:             "超长行分段截断后跨段续读",
			content:          strings.Repeat("X", longLine) + "\n",
			nMax:             4096,
			linesMax:         100,
			wantContent:      strings.Repeat("X", 4096),
			wantLines:        0,
			wantEndLine:      1,
			wantTrunc:        true,
			wantTruncN:       4096,
			wantNext:         strings.Repeat("X", longLine-4096) + "\n",
			wantNextLines:    1,
			wantNextEndLine:  1,
			wantNextFinished: true,
		},
		{
			// 超长行完整读完，但行尾 \n 超出预算，同样按截断处理
			name:             "超长行读完内容后换行符超出预算",
			content:          strings.Repeat("X", longLine),
			nMax:             5000,
			linesMax:         100,
			wantContent:      strings.Repeat("X", longLine),
			wantLines:        0,
			wantEndLine:      1,
			wantTrunc:        true,
			wantTruncN:       5000,
			wantNext:         "\n",
			wantNextLines:    1,
			wantNextEndLine:  1,
			wantNextFinished: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := normalizedContent(tc.content)

			res := readPagedStr(tc.content, tc.nMax, tc.linesMax, 1, 1)
			if res.Err != nil {
				t.Fatalf("unexpected err: %v", res.Err)
			}
			if got := string(res.Content); got != tc.wantContent {
				t.Errorf("首屏 Content:\n got  %q\n want %q", got, tc.wantContent)
			}
			if len(res.Content) > tc.nMax {
				t.Errorf("首屏 len(Content)=%d 超过 MaxBytes=%d", len(res.Content), tc.nMax)
			}
			if res.LinesRead != tc.wantLines {
				t.Errorf("LinesRead = %d, want %d", res.LinesRead, tc.wantLines)
			}
			if res.EndLineNo != tc.wantEndLine {
				t.Errorf("EndLineNo = %d, want %d", res.EndLineNo, tc.wantEndLine)
			}
			if res.LastLineTruncated != tc.wantTrunc {
				t.Errorf("LastLineTruncated = %v, want %v", res.LastLineTruncated, tc.wantTrunc)
			}
			if res.LastLineTruncatedBytes != tc.wantTruncN {
				t.Errorf("LastLineTruncatedBytes = %d, want %d", res.LastLineTruncatedBytes, tc.wantTruncN)
			}
			if res.Finished != tc.wantFinished {
				t.Errorf("Finished = %v, want %v", res.Finished, tc.wantFinished)
			}

			// 按既有续读约定取下一屏
			startLine, startBytes := res.EndLineNo+1, 1
			if res.LastLineTruncated {
				startLine, startBytes = res.EndLineNo, res.LastLineTruncatedBytes+1
			}
			next := readPagedStr(tc.content, tc.nMax, tc.linesMax, startLine, startBytes)
			if got := string(next.Content); got != tc.wantNext {
				t.Errorf("续读（start_line_no=%d, start_bytes=%d）Content:\n got  %q\n want %q",
					startLine, startBytes, got, tc.wantNext)
			}
			if len(next.Content) > tc.nMax {
				t.Errorf("续读 len(Content)=%d 超过 MaxBytes=%d", len(next.Content), tc.nMax)
			}
			if next.LinesRead != tc.wantNextLines {
				t.Errorf("续读 LinesRead = %d, want %d", next.LinesRead, tc.wantNextLines)
			}
			if next.EndLineNo != tc.wantNextEndLine {
				t.Errorf("续读 EndLineNo = %d, want %d", next.EndLineNo, tc.wantNextEndLine)
			}
			if next.LastLineTruncated != tc.wantNextTrunc {
				t.Errorf("续读 LastLineTruncated = %v, want %v", next.LastLineTruncated, tc.wantNextTrunc)
			}
			if next.LastLineTruncatedBytes != tc.wantNextTruncN {
				t.Errorf("续读 LastLineTruncatedBytes = %d, want %d", next.LastLineTruncatedBytes, tc.wantNextTruncN)
			}
			if next.Finished != tc.wantNextFinished {
				t.Errorf("续读 Finished = %v, want %v", next.Finished, tc.wantNextFinished)
			}

			// 全程翻页拼接无损
			if got, _ := paginate(t, tc.content, tc.nMax, tc.linesMax); got != want {
				t.Errorf("翻页拼接与规范化原文件不一致:\n got  %q\n want %q", got, want)
			}
		})
	}
}

// TestReadPagedMaxBytesSweep 穷举小预算组合，验证字节上限与无损续读。
func TestReadPagedMaxBytesSweep(t *testing.T) {
	shortContents := []string{
		"",
		"\n",
		"a",
		"a\n",
		"ab\nc",
		"abcd\nefgh\n",
		"aaa\n\n\nbbb\n\n",
		"aaa\r\nbb\r\n\r\nccc",
		strings.Repeat("a\n", 12),
		"ab\n\ncdef\n\n\ngh",
	}
	lineLimits := []int{1, 2, 3, 1000}

	for _, content := range shortContents {
		want := normalizedContent(content)
		for nMax := 1; nMax <= len(content)+2; nMax++ {
			for _, linesMax := range lineLimits {
				got, _ := paginate(t, content, nMax, linesMax)
				if got != want {
					t.Fatalf("content=%q nMax=%d linesMax=%d 翻页拼接不一致:\n got  %q\n want %q",
						content, nMax, linesMax, got, want)
				}
			}
		}
	}
}
