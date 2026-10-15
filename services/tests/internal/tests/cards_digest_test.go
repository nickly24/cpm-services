package tests

import (
	"strings"
	"testing"
)

func TestCardsDigestLegacyFingerprintAndAnswerShapes(t *testing.T) {
	if got := cardsFingerprint(" Вопрос <b> & </b> ", " Ответ\n\nстрока "); got != "f96387df39397fdd0256eb168927c7443c53c05660148047494c5e2fd9ae27af" {
		t.Fatal("existing progress fingerprint changed", got)
	}
	for _, tc := range []struct {
		q    Question
		want string
	}{
		{Question{Type: "single", Answers: []AnswerOption{{Text: " A ", IsCorrect: true}, {Text: "B", IsCorrect: true}}}, "A"},
		{Question{Type: "multiple", Answers: []AnswerOption{{Text: " A ", IsCorrect: true}, {Text: "B", IsCorrect: true}}}, "A; B"},
		{Question{Type: "text", CorrectAnswers: []string{" One\nTwo ", " Alias "}}, "One\nTwo / Alias"},
	} {
		if cardsAnswer(tc.q) != tc.want {
			t.Fatal("answer formatting differs from Cards", cardsAnswer(tc.q), tc.want)
		}
	}
	if cardsFingerprint("Q", "A\nB") == cardsFingerprint("Q", "A B") || !strings.EqualFold(cardsFingerprint(" Q ", " A "), cardsFingerprint("Q", "A")) {
		t.Fatal("fingerprint whitespace semantics changed")
	}
}
