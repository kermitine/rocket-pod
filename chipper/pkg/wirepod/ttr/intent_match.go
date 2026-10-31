package wirepod_ttr

import (
	"strings"
	"unicode"
)

// Normalize only the matching copy. Keep punctuation and spacing in the
// transcription passed to parameter parsing, plugins, and intent responses.
func normalizeIntentMatchText(text string) string {
	text = strings.Map(func(r rune) rune {
		switch r {
		case '\'', '\u2018', '\u2019', '\u02bc':
			return '\'' // Preserve contractions, including typographic apostrophes.
		}
		if unicode.IsPunct(r) {
			return ' ' // Separate words instead of joining them across punctuation.
		}
		return unicode.ToLower(r)
	}, text)
	text = strings.TrimFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\''
	})
	return strings.Join(strings.Fields(text), " ")
}

func matchesExactIntentPhrase(voiceText, keyphrase string) bool {
	voiceText = normalizeIntentMatchText(voiceText)
	keyphrase = normalizeIntentMatchText(keyphrase)
	return keyphrase != "" && voiceText == keyphrase
}

func matchesPartialIntentPhrase(voiceText, keyphrase, intent string) bool {
	voiceText = normalizeIntentMatchText(voiceText)
	keyphrase = normalizeIntentMatchText(keyphrase)
	if keyphrase == "" {
		return false
	}
	if intent == "intent_imperative_affirmative" || intent == "intent_imperative_negative" {
		// Preserve matching for scripts that do not separate words with spaces.
		unspacedScript := strings.IndexFunc(keyphrase, func(r rune) bool {
			return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
		}) >= 0
		// Short replies such as "yes", "ok", and "sure" must not match inside
		// unrelated words such as "yesterday", "book", and "pressure".
		if !unspacedScript {
			return strings.Contains(" "+voiceText+" ", " "+keyphrase+" ")
		}
	}
	// Other intents have deliberate partial-word keyphrases for STT variants.
	return strings.Contains(voiceText, keyphrase)
}
