package main

import "testing"

func TestFacePixelFromBytesUsesSDKByteOrder(t *testing.T) {
	tests := []struct {
		name  string
		bytes []byte
		want  uint16
	}{
		{name: "red", bytes: []byte{0xF8, 0x00}, want: 0xF800},
		{name: "green", bytes: []byte{0x07, 0xE0}, want: 0x07E0},
		{name: "blue", bytes: []byte{0x00, 0x1F}, want: 0x001F},
		{name: "white", bytes: []byte{0xFF, 0xFF}, want: 0xFFFF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := facePixelFromBytes(tt.bytes); got != tt.want {
				t.Fatalf("facePixelFromBytes() = %#04x, want %#04x", got, tt.want)
			}
		})
	}
}

func TestFaceImagePixelCount(t *testing.T) {
	tests := []struct {
		name       string
		byteCount  int
		wantPixels int
		wantOK     bool
	}{
		{
			name:       "Vector 1",
			byteCount:  vector1FaceImagePixels * faceImageBytesPerPixel,
			wantPixels: vector1FaceImagePixels,
			wantOK:     true,
		},
		{
			name:       "Vector 2",
			byteCount:  vector2FaceImagePixels * faceImageBytesPerPixel,
			wantPixels: vector2FaceImagePixels,
			wantOK:     true,
		},
		{
			name:      "Invalid",
			byteCount: vector2FaceImagePixels*faceImageBytesPerPixel - 1,
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPixels, gotOK := faceImagePixelCount(make([]byte, tt.byteCount))
			if gotPixels != tt.wantPixels || gotOK != tt.wantOK {
				t.Fatalf("faceImagePixelCount() = (%d, %v), want (%d, %v)", gotPixels, gotOK, tt.wantPixels, tt.wantOK)
			}
		})
	}
}

func TestFaceImageChunkCount(t *testing.T) {
	tests := []struct {
		name        string
		totalPixels int
		wantChunks  int
	}{
		{name: "Vector 1", totalPixels: vector1FaceImagePixels, wantChunks: 30},
		{name: "Vector 2", totalPixels: vector2FaceImagePixels, wantChunks: 22},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := faceImageChunkCount(tt.totalPixels); got != tt.wantChunks {
				t.Fatalf("faceImageChunkCount(%d) = %d, want %d", tt.totalPixels, got, tt.wantChunks)
			}
		})
	}
}
