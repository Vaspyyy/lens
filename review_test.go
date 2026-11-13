package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for findings of the reviews.

// GPT-6.1 Terra: a zero-sized data chunk in a finished file is empty, not
// a stream that runs into the chunks after it.
func TestReviewEmptyDataChunk(t *testing.T) {
	chunk := func(id string, b []byte) []byte {
		var c bytes.Buffer
		c.WriteString(id)
		binary.Write(&c, binary.LittleEndian, uint32(len(b)))
		c.Write(b)
		if len(b)%2 == 1 {
			c.WriteByte(0)
		}
		return c.Bytes()
	}
	var f bytes.Buffer
	binary.Write(&f, binary.LittleEndian, []uint16{1, 1})
	binary.Write(&f, binary.LittleEndian, []uint32{8000, 16000})
	binary.Write(&f, binary.LittleEndian, []uint16{2, 16})
	body := append([]byte("WAVE"), chunk("fmt ", f.Bytes())...)
	body = append(body, chunk("data", nil)...)
	body = append(body, chunk("LIST", []byte("12345678"))...)
	var out bytes.Buffer
	out.WriteString("RIFF")
	binary.Write(&out, binary.LittleEndian, uint32(len(body)))
	out.Write(body)
	p := filepath.Join(t.TempDir(), "empty.wav")
	os.WriteFile(p, out.Bytes(), 0o644)
	got, _ := lensRun(t, p)
	mustContain(t, got, "samples   none")
	mustLack(t, got, "peak")
}

// GPT-6.1 Terra: a truncated MP3 says so instead of passing for a short one.
func TestReviewTruncatedMP3(t *testing.T) {
	raw, err := os.ReadFile("testdata/tone.mp3")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "cut.mp3")
	os.WriteFile(p, raw[:len(raw)/2], 0o644)
	out, code := lensRun(t, p)
	if code != 2 {
		t.Fatalf("exit %d: %s", code, out)
	}
	mustContain(t, out, "warning   the file ends early: its LAME header promises 490 ms of sound")
}

// GPT-6.1 Terra: with more spots than are listed, the crop still covers all
// of them.
func TestReviewCropCoversEverySpot(t *testing.T) {
	dir := t.TempDir()
	a := canvas(500, 500, white)
	b := canvas(500, 500, white)
	for _, p := range [][2]int{{10, 10}, {100, 10}, {200, 10}, {300, 10}, {400, 10}, {10, 400}, {400, 400}} {
		b.SetNRGBA(p[0], p[1], black)
	}
	out, _ := lensRun(t, "diff", savePNG(t, dir, "a.png", opaqueRGB(a)), savePNG(t, dir, "b.png", opaqueRGB(b)))
	mustContain(t, out, "in 7 spots", "… 1 more spot", "crop     417x417+0+0 for both")
	mustLack(t, out, "one per spot")
	d := compare(mustLoad(t, filepath.Join(dir, "a.png")), mustLoad(t, filepath.Join(dir, "b.png")), options{fuzz: -1, pad: 16})
	c := cropFor(d, options{pad: 16})
	for _, s := range d.spots {
		in := false
		for _, box := range c.boxes {
			in = in || s.box.In(box)
		}
		if !in {
			t.Errorf("spot %v is in no crop %v", s.box, c.boxes)
		}
	}
}

func mustLoad(t *testing.T, p string) *imageFacts {
	t.Helper()
	f, err := loadImage(p, sniffFile(p))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// GPT-6.1 Terra: NaN and infinities aren't levels or percents.
func TestReviewNaNFlags(t *testing.T) {
	for _, args := range [][]string{
		{"diff", "--fuzz", "NaN", "a.png", "b.png"},
		{"diff", "--fuzz", "Inf", "a.png", "b.png"},
		{"a.wav", "--silence", "NaN"},
		{"a.wav", "--silence", "-Inf"},
	} {
		out, code := lensRun(t, args...)
		if code != 2 || !strings.Contains(out, "takes") {
			t.Errorf("%v: exit %d, %q", args, code, out)
		}
	}
	_ = image.Point{}
}
