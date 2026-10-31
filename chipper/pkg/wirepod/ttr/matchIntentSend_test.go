package wirepod_ttr

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	pb "github.com/digital-dream-labs/api/go/chipperpb"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/vtt"
)

type recordingIntentStream struct {
	pb.ChipperGrpc_StreamingIntentServer
	record func(*pb.IntentResult)
}

func (s *recordingIntentStream) Send(response *pb.IntentResponse) error {
	s.record(response.IntentResult)
	return nil
}

type recordingIntentGraphStream struct {
	pb.ChipperGrpc_StreamingIntentGraphServer
	record func(*pb.IntentResult)
}

func (s *recordingIntentGraphStream) Send(response *pb.IntentGraphResponse) error {
	s.record(response.IntentResult)
	return nil
}

func TestProcessTextAllSpokenReplies(t *testing.T) {
	data, err := os.ReadFile("../../../intent-data/en-US.json")
	if err != nil {
		t.Fatal(err)
	}
	var intents []vars.JsonIntent
	if err := json.Unmarshal(data, &intents); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		text, want string
	}{
		{"yes.", "intent_imperative_affirmative"},
		{"YES!", "intent_imperative_affirmative"},
		{"Yes, please!", "intent_imperative_affirmative"},
		{"Yep!", "intent_imperative_affirmative"},
		{"Yup!", "intent_imperative_affirmative"},
		{"Yeah.", "intent_imperative_affirmative"},
		{"Sure!", "intent_imperative_affirmative"},
		{"OK.", "intent_imperative_affirmative"},
		{"Okay!", "intent_imperative_affirmative"},
		{"Correct.", "intent_imperative_affirmative"},
		{"Absolutely!", "intent_imperative_affirmative"},
		{"Of course.", "intent_imperative_affirmative"},
		{"Go\t\n ahead!", "intent_imperative_affirmative"},
		{"I said yes!", "intent_imperative_affirmative"},
		{"no.", "intent_imperative_negative"},
		{"NO!", "intent_imperative_negative"},
		{"Nope!", "intent_imperative_negative"},
		{"Nah.", "intent_imperative_negative"},
		{"No, thanks!", "intent_imperative_negative"},
		{"No, thank you.", "intent_imperative_negative"},
		{"Don\u2019t!", "intent_imperative_negative"},
		{"Not really.", "intent_imperative_negative"},
		{"I said no!", "intent_imperative_negative"},
		{"No more cards!", "intent_imperative_negative"},
		{"Hit me!", "intent_blackjack_hit"},
		{"Stand!", "intent_blackjack_stand"},
		{"Hello!", "intent_greeting_hello"},
		{"Stop!", "intent_imperative_quiet"},
		{"Go, left!", "intent_imperative_turnleft"},
		{"Could you go, left, please?", "intent_imperative_turnleft"},
		{"Look at me please.", "intent_imperative_lookatme"},
		{"yesterday", ""},
		{"book", ""},
		{"pressure", ""},
		{"incorrect", ""},
		{"notification", ""},
		{"...?!", ""},
	}
	for _, mode := range []struct {
		name            string
		isGraph, isOpus bool
	}{
		{"intent/opus", false, true},
		{"intent/pcm", false, false},
		{"intent-graph/opus", true, true},
		{"intent-graph/pcm", true, false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.text, func(t *testing.T) {
					var results []*pb.IntentResult
					record := func(result *pb.IntentResult) { results = append(results, result) }
					var request interface{}
					if mode.isGraph {
						request = &vtt.IntentGraphRequest{Device: "test-spoken-replies", Stream: &recordingIntentGraphStream{record: record}}
					} else {
						request = &vtt.IntentRequest{Device: "test-spoken-replies", Stream: &recordingIntentStream{record: record}}
					}
					matched := ProcessTextAll(request, tt.text, intents, mode.isOpus)
					if tt.want == "" {
						if matched || len(results) != 0 {
							t.Fatalf("unrelated text %q matched: %v", tt.text, results)
						}
						return
					}
					if !matched || len(results) != 1 {
						t.Fatalf("%q: matched=%v, received %d results; want %s", tt.text, matched, len(results), tt.want)
					}
					result := results[0]
					if result.Action != tt.want {
						t.Errorf("%q: intent = %s, want %s", tt.text, result.Action, tt.want)
					}
					if result.QueryText != strings.ToLower(tt.text) {
						t.Errorf("matching changed the response transcription: %q", result.QueryText)
					}
				})
			}
		})
	}
}
