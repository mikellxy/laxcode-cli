package context

import (
	"unicode"
)

// EstimateToken 粗估算token
// 英文(ASCII字母数字符号): 4 char = 1 token
// 中文汉字: 1.5 rune = 1 token
func EstimateToken(s string) float64 {
	var asciiCount int
	var cnCount int

	for _, r := range s {
		// 汉字范围
		if unicode.Is(unicode.Han, r) {
			cnCount++
		} else {
			// 英文、数字、空格、标点统一算作ascii字符
			asciiCount++
		}
	}

	tokens := float64(asciiCount)/4.0 + float64(cnCount)/1.5
	return tokens
}

// EstimateTokenInt 返回向上取整整数token数量
func EstimateTokenInt(s string) int {
	t := EstimateToken(s)
	return int(t) + 1
}
