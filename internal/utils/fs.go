package utils

import (
	"bufio"
	"io"
	"os"
)

// ReadResult is the paginated read result of ReadUpToNKB.
// Pagination convention for the next call:
//   - not truncated: pass startLineNo=EndLineNo+1, startBytes=1
//   - line truncated (LastLineTruncated=true): pass startLineNo=EndLineNo,
//     startBytes=LastLineTruncatedBytes+1
type ReadResult struct {
	Path                   string `json:"path"`
	Content                []byte `json:"-"`
	LinesRead              int    `json:"linesRead"` // complete lines read in this call; the truncated last line is not counted
	StartLineNo            int    `json:"startLineNo"`
	EndLineNo              int    `json:"endLineNo"`              // last line number touched in this call
	LastLineTruncated      bool   `json:"lastLineTruncated"`      // whether the last line was not fully read
	LastLineTruncatedBytes int    `json:"lastLineTruncatedBytes"` // consumed byte offset within the truncated line (newline excluded); resume with +1
	Finished               bool   `json:"finished"`               // whether EOF has been reached
	Err                    error  `json:"err"`
}

// ReadUpToNKB reads path starting at line startLineNo (1-based) and byte
// startBytes (1-based) within that line, keeping at most nMax bytes of content
// and linesMax lines (nMax and linesMax must be positive).
// Every line in Content ends with \n (including a file's last line that has no
// trailing newline); \r\n is normalized to \n; lines longer than the bufio
// buffer are read in segments and concatenated.
func ReadUpToNKB(nMax, linesMax, startLineNo, startBytes int, path string) (result *ReadResult) {
	// both line number and byte offset are 1-based; clamp invalid values to the line start
	if startLineNo < 1 {
		startLineNo = 1
	}
	if startBytes < 1 {
		startBytes = 1
	}

	result = &ReadResult{
		Path:        path,
		StartLineNo: startLineNo,
		Content:     make([]byte, 0, nMax),
	}

	f, err := os.Open(path)
	if err != nil {
		result.Err = err
		return
	}
	defer f.Close()
	fr := bufio.NewReader(f)

	var (
		curLineNo            int
		lineOpen             bool
		linesRead            int
		bytesSkipInStartLine int
		nRead                int
		lineConsumed         int
	)

	for {
		if nRead >= nMax || linesRead >= linesMax {
			break
		}

		line, isPrefix, err := fr.ReadLine()
		if err != nil {
			if err == io.EOF {
				result.Finished = true
				break
			}
			result.Err = err
			return
		}

		// Increment the line number at line start: segments of an overlong
		// line (isPrefix=true) only end at the line's end; counting there
		// would let the first segment inherit the previous line's number
		// and be skipped as a line before startLineNo
		if !lineOpen {
			curLineNo++
			lineOpen = true
			lineConsumed = 0
		}
		if !isPrefix {
			lineOpen = false
		}

		// skip lines not required
		if curLineNo < startLineNo {
			continue
		}

		// for the start line, skip bytes not needed
		if curLineNo == startLineNo && bytesSkipInStartLine < startBytes-1 {
			trySkip := min(len(line), startBytes-1-bytesSkipInStartLine)
			line = line[trySkip:]
			bytesSkipInStartLine += trySkip
			lineConsumed += trySkip
		}

		// keep up to nMax KB
		nKeep := min(len(line), nMax-nRead)
		result.Content = append(result.Content, line[:nKeep]...)
		if !isPrefix && nKeep == len(line) {
			result.Content = append(result.Content, byte('\n'))
			linesRead++
		}
		nRead += nKeep
		lineConsumed += nKeep

		// if last line need to be truncated
		result.LastLineTruncated = isPrefix || nKeep < len(line)
		if result.LastLineTruncated {
			result.LastLineTruncatedBytes = lineConsumed
		} else {
			result.LastLineTruncatedBytes = 0
		}
	}

	result.LinesRead = linesRead
	result.EndLineNo = curLineNo
	return
}
