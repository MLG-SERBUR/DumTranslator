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
// DetectLanguage returns "ar" if Arabic, "ko" if Korean, or "unknown" otherwise.
func DetectLanguage(text string) string {
	var arCount, korCount, letterCount int

	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		letterCount++
		if unicode.In(r, unicode.Arabic) {
			arCount++
		} else if unicode.In(r, unicode.Hangul) {
			korCount++
		}
	}

	if letterCount == 0 {
		return "unknown"
	}

	if float64(arCount)/float64(letterCount) > 0.4 {
		return "ar"
	}
	if float64(korCount)/float64(letterCount) > 0.4 {
		return "ko"
	}

	return "unknown"
}
