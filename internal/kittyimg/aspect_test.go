package kittyimg

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"testing"
)

func TestAspect(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		want          float64
	}{
		{name: "square", width: 4, height: 4, want: 1},
		{name: "wide", width: 6, height: 3, want: 2},
		{name: "tall", width: 2, height: 8, want: 0.25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw bytes.Buffer
			if err := png.Encode(&raw, image.NewNRGBA(image.Rect(0, 0, tt.width, tt.height))); err != nil {
				t.Fatalf("encode: %v", err)
			}
			source := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw.Bytes())
			got, err := Aspect(source)
			if err != nil || got != tt.want {
				t.Fatalf("Aspect = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

func TestAspectRejectsNonImage(t *testing.T) {
	if _, err := Aspect("data:text/plain;base64," + base64.StdEncoding.EncodeToString([]byte("nope"))); err == nil {
		t.Fatal("Aspect accepted a non-image source")
	}
}
