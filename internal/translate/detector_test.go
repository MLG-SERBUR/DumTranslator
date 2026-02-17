package translate

import "testing"

func TestIsArabicOrKorean(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"Arabic", "مرحبا كيف حالك", true},
		{"Korean", "안녕하세요", true},
		{"English", "Hello world", false},
		{"French", "Bonjour le monde", false},
		{"Mixed Arabic High Density", "Hello مرحبا", true}, // 5 Arabic / 10 Total letters = 0.5 > 0.4 -> True
        {"Mixed Arabic Low Density", "Hello world م", false}, // 1 Arabic / 11 Total letters < 0.4 -> False
        {"Clear Arabic", "السماء زرقاء والشمس مشرقة", true},
        {"Clear Korean", "나는 학교에 갑니다", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsArabicOrKorean(tt.text); got != tt.want {
				t.Errorf("IsArabicOrKorean(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}
