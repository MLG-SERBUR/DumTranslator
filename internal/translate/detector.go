package translate

import (
	"unicode"
)

// IsArabicOrKorean returns true if the text contains a significant portion of Arabic or Korean characters.
func IsArabicOrKorean(text string) bool {
	// Heuristic: If > 40% of letters are Arabic or Korean, return true.
	var arKorCount, letterCount int

	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		letterCount++
		if unicode.In(r, unicode.Arabic) || unicode.In(r, unicode.Hangul) {
			arKorCount++
		}
	}

	if letterCount == 0 {
		return false
	}

	return float64(arKorCount)/float64(letterCount) > 0.4
}
