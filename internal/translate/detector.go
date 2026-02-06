package translate

import (
	"github.com/abadojack/whatlanggo"
)

// IsEnglish returns true if the text is detected as English.
// It returns false if it is any other language.
// If confidence is low, we might default to one way or another, 
// but whatlanggo is usually decent.
func IsEnglish(text string) bool {
	info := whatlanggo.Detect(text)
	return info.Lang == whatlanggo.Eng
}
