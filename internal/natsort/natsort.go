package natsort

import (
	"strconv"
	"strings"
	"unicode"
)

func Less(a, b string) bool {
	ar, br := []rune(strings.ToLower(a)), []rune(strings.ToLower(b))
	for i, j := 0, 0; i < len(ar) && j < len(br); {
		if unicode.IsDigit(ar[i]) && unicode.IsDigit(br[j]) {
			ai, aj := i, j
			for ai < len(ar) && unicode.IsDigit(ar[ai]) {
				ai++
			}
			for aj < len(br) && unicode.IsDigit(br[aj]) {
				aj++
			}
			an, _ := strconv.ParseUint(string(ar[i:ai]), 10, 64)
			bn, _ := strconv.ParseUint(string(br[j:aj]), 10, 64)
			if an != bn {
				return an < bn
			}
			if ai-i != aj-j {
				return ai-i < aj-j
			}
			i, j = ai, aj
			continue
		}
		if ar[i] != br[j] {
			return ar[i] < br[j]
		}
		i++
		j++
		if i == len(ar) || j == len(br) {
			return len(ar) < len(br)
		}
	}
	return len(ar) < len(br)
}
