package scripting

import "testing"

func TestConvertPixelsToRGB565(t *testing.T) {
	tests := []struct {
		name    string
		r, g, b uint32
		want    uint16
	}{
		{name: "red", r: 0xffff, want: 0xf800},
		{name: "green", g: 0xffff, want: 0x07e0},
		{name: "blue", b: 0xffff, want: 0x001f},
		{name: "white", r: 0xffff, g: 0xffff, b: 0xffff, want: 0xffff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConvertPixesTo16BitRGB(tt.r, tt.g, tt.b, 0xffff, 100); got != tt.want {
				t.Fatalf("ConvertPixesTo16BitRGB() = %#04x, want %#04x", got, tt.want)
			}
		})
	}
}
