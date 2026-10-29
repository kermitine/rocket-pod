package native

import "testing"

func TestInvalidModelReturnsError(t *testing.T) {
	model, err := NewModel(t.TempDir())
	if err == nil || model != nil {
		t.Fatalf("invalid model did not return an error: model=%v, error=%v", model, err)
	}
}
