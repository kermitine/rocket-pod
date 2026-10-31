package wirepod_ttr

import (
	"encoding/json"
	"os"
	"testing"
)

func TestMatchesExactIntentPhrase(t *testing.T) {
	tests := []struct {
		text, phrase string
		want         bool
	}{
		{"no", "no", true},
		{"no.", "no", true},
		{"no!", "no", true},
		{"  No?!\n", "no", true},
		{"\u201cNo!\u201d", "no", true},
		{"no\u3002", "no", true},
		{"yes.", "yes", true},
		{"YES!", "yes", true},
		{"Yes, please!", "yes please", true},
		{"No, thank you.", "no thank you", true},
		{"Go\t\n ahead!", "go ahead", true},
		{"No\u00a0thanks!", "no thanks", true},
		{"Don't!", "don't", true},
		{"Don\u2019t!", "don't", true},
		{"\u2018Don\u2019t!\u2019", "don't", true},
		{"don't", "dont", false},
		{"n.o", "no", false},
		{"i said no!", "no", false},
		{"no more cards!", "no more cards", true},
		{"know", "no", false},
		{"notification", "no", false},
		{"yesterday", "yes", false},
		{"book", "ok", false},
		{"pressure", "sure", false},
		{"what's the weather in norway?", "no", false},
		{"no thanks, play blackjack", "no thanks", false},
		{"no! yes!", "no", false},
		{"...", "no", false},
		{"", "", false},
		{"...", "!", false},
	}
	for _, tt := range tests {
		t.Run(tt.text+"/"+tt.phrase, func(t *testing.T) {
			if got := matchesExactIntentPhrase(tt.text, tt.phrase); got != tt.want {
				t.Errorf("matchesExactIntentPhrase(%q, %q) = %v, want %v", tt.text, tt.phrase, got, tt.want)
			}
		})
	}
}

func TestEnglishNegativeReplies(t *testing.T) {
	data, err := os.ReadFile("../../../intent-data/en-US.json")
	if err != nil {
		t.Fatal(err)
	}
	var intents []struct {
		Name              string   `json:"name"`
		Keyphrases        []string `json:"keyphrases"`
		RequireExactMatch bool     `json:"requiresexact"`
	}
	if err := json.Unmarshal(data, &intents); err != nil {
		t.Fatal(err)
	}
	for _, intent := range intents {
		if intent.Name != "intent_imperative_negative" {
			continue
		}
		if !intent.RequireExactMatch {
			t.Fatal("negative replies must still require an exact phrase match")
		}
		for _, text := range []string{"no", "no.", "no!", "i said no!", "No more cards.", "No thank you!", "No, thank you!", "Don't!", "Don\u2019t!"} {
			t.Run(text, func(t *testing.T) {
				for _, phrase := range intent.Keyphrases {
					if matchesExactIntentPhrase(text, phrase) {
						return
					}
				}
				t.Errorf("%q did not match the English negative intent", text)
			})
		}
		return
	}
	t.Fatal("English negative intent is missing")
}

func TestPartialReplyWordBoundaries(t *testing.T) {
	for _, tt := range []struct {
		text, phrase string
		want         bool
	}{
		{"Yes, please!", "yes", true},
		{"I said yes!", "yes", true},
		{"Okay, go ahead.", "go ahead", true},
		{"yes! yes!", "yes", true},
		{"yesterday", "yes", false},
		{"book", "ok", false},
		{"pressure", "sure", false},
		{"incorrect", "correct", false},
		{"yes's", "yes", false},
		{"...", "", false},
		{"\u662f\u7684\u3002", "\u662f", true},
		{"\u5f53\u7136\u53ef\u4ee5\u3002", "\u5f53\u7136", true},
	} {
		t.Run(tt.text+"/"+tt.phrase, func(t *testing.T) {
			if got := matchesPartialIntentPhrase(tt.text, tt.phrase, "intent_imperative_affirmative"); got != tt.want {
				t.Errorf("partial reply match(%q, %q) = %v, want %v", tt.text, tt.phrase, got, tt.want)
			}
		})
	}
	if !matchesPartialIntentPhrase("Please roll your cube!", "roll your cu", "intent_play_rollcube") {
		t.Fatal("deliberate partial-word keyphrases must still work for other intents")
	}
}
