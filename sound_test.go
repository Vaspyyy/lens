package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// field is the number after a label in lens's output: "peak -20.0 dBFS".
func field(t *testing.T, out, re string) float64 {
	t.Helper()
	m := regexp.MustCompile(re).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no %q in:\n%s", re, out)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func near64(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v ± %v", what, got, want, tol)
	}
}

func TestWAVTone(t *testing.T) {
	const r = 48000
	p := filepath.Join(t.TempDir(), "tone.wav")
	x := cat(silence(r, 0.1), sine(r, 997, 1.0, -20), silence(r, 0.3))
	writeWAV(t, p, wavSpec{rate: r}, x)
	out, code := lensRun(t, p)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mustContain(t, out, "WAV PCM 16-bit, 1.40 s, 48 kHz mono", "no clipping",
		"100 ms at the start, 300 ms at the end (below -60 dBFS)",
		"pitched, strongest at 997 Hz (B5)", "waveform  │")
	near64(t, "peak", field(t, out, `peak (-?[\d.]+) dBFS`), -20, 0.05)
	near64(t, "RMS", field(t, out, `RMS (-?[\d.]+) dBFS`), -20-3.01+10*math.Log10(1.0/1.4), 0.1)
	mustLack(t, out, "stereo ")
	// BS.1770: a 997 Hz sine at 0 dBFS in one channel is -3.01 LUFS
	writeWAV(t, p, wavSpec{rate: r}, sine(r, 997, 2, -20))
	out, _ = lensRun(t, p)
	near64(t, "LUFS", field(t, out, `loudness (-?[\d.]+) LUFS`), -23.01, 0.1)
}

func TestWAVFormats(t *testing.T) {
	const r = 22050
	x := sine(r, 440, 0.5, -6)
	for _, spec := range []wavSpec{
		{rate: r, bits: 8}, {rate: r, bits: 16}, {rate: r, bits: 24}, {rate: r, bits: 32},
		{rate: r, bits: 32, float: true}, {rate: r, bits: 64, float: true},
		{rate: r, bits: 24, extensible: true}, {rate: r, bits: 32, float: true, extensible: true},
	} {
		p := filepath.Join(t.TempDir(), "f.wav")
		writeWAV(t, p, spec, x)
		out, code := lensRun(t, p)
		if code != 0 {
			t.Errorf("%+v: exit %d: %s", spec, code, out)
			continue
		}
		want := "PCM " + strconv.Itoa(spec.bits) + "-bit"
		if spec.float {
			want = "float " + strconv.Itoa(spec.bits) + "-bit"
		}
		mustContain(t, out, want, "500 ms, 22.05 kHz mono")
		tol := 0.05
		if spec.bits == 8 {
			tol = 0.1
		}
		near64(t, want+" peak", field(t, out, `peak (-?[\d.]+) dBFS`), -6, tol)
	}
}

func TestWAVStreamAndTruncated(t *testing.T) {
	const r = 8000
	x := sine(r, 500, 1, -10)
	dir := t.TempDir()
	stream := filepath.Join(dir, "stream.wav")
	writeWAV(t, stream, wavSpec{rate: r, dataSize: 0xffffffff}, x)
	out, _ := lensRun(t, stream)
	mustContain(t, out, "1.00 s, 8 kHz mono")
	cut := filepath.Join(dir, "cut.wav")
	writeWAV(t, cut, wavSpec{rate: r}, x)
	raw, _ := os.ReadFile(cut)
	os.WriteFile(cut, raw[:len(raw)-8000], 0o644) // half the data is gone
	out, code := lensRun(t, cut)
	if code != 2 {
		t.Fatalf("a truncated file: exit %d: %s", code, out)
	}
	mustContain(t, out, "500 ms, 8 kHz mono", "warning   the file ends early: its header promises 1.00 s of sound, the file holds 500 ms")

	// RF64: the sizes are in a ds64 chunk
	plain := filepath.Join(dir, "plain.wav")
	writeWAV(t, plain, wavSpec{rate: r}, x)
	raw, _ = os.ReadFile(plain)
	fmtAt := bytes.Index(raw, []byte("fmt "))
	dataAt := bytes.Index(raw, []byte("data"))
	var rf bytes.Buffer
	rf.WriteString("RF64\xff\xff\xff\xffWAVEds64")
	binary.Write(&rf, binary.LittleEndian, uint32(28))
	binary.Write(&rf, binary.LittleEndian, uint64(0))
	binary.Write(&rf, binary.LittleEndian, uint64(len(raw)-dataAt-8))
	binary.Write(&rf, binary.LittleEndian, uint64(0))
	binary.Write(&rf, binary.LittleEndian, uint32(0))
	rf.Write(raw[fmtAt:dataAt])
	rf.WriteString("data\xff\xff\xff\xff")
	rf.Write(raw[dataAt+8:])
	rf.Write([]byte("junk")) // past the data: not samples
	rf64 := filepath.Join(dir, "rf.wav")
	os.WriteFile(rf64, rf.Bytes(), 0o644)
	out, code = lensRun(t, rf64)
	if code != 0 {
		t.Fatalf("RF64: exit %d: %s", code, out)
	}
	mustContain(t, out, "WAV PCM 16-bit, 1.00 s, 8 kHz mono")
}

func TestStereoCases(t *testing.T) {
	const r = 16000
	a := sine(r, 300, 0.6, -12)
	neg := make([]float64, len(a))
	quiet := make([]float64, len(a))
	for i, v := range a {
		neg[i] = -v
		quiet[i] = v * 0.3
	}
	other := sine(r, 470, 0.6, -12)
	for _, c := range []struct {
		l, r []float64
		want string
	}{
		{a, a, "both channels the same (mono in stereo)"},
		{a, silence(r, 0.6), "the right channel is silent"},
		{silence(r, 0.6), a, "the left channel is silent"},
		{a, neg, "opposite phase (correlation -1.00)"},
		{a, quiet, "left 10.5 dB louder than right"},
		{a, other, "the channels differ (correlation"},
	} {
		p := filepath.Join(t.TempDir(), "s.wav")
		writeWAV(t, p, wavSpec{rate: r}, c.l, c.r)
		out, _ := lensRun(t, p)
		mustContain(t, out, "stereo    "+c.want)
	}
}

func TestClipping(t *testing.T) {
	const r = 8000
	x := sine(r, 200, 0.5, 6) // twice full scale: clipped flat tops
	p := filepath.Join(t.TempDir(), "clip.wav")
	writeWAV(t, p, wavSpec{rate: r}, x)
	out, _ := lensRun(t, p)
	mustContain(t, out, "clipped: ", "at full scale in 200 runs")
	f := filepath.Join(t.TempDir(), "over.wav")
	writeWAV(t, f, wavSpec{rate: r, bits: 32, float: true}, x)
	out, _ = lensRun(t, f)
	mustContain(t, out, "over full scale (float)")
}

func TestSilentAndPauses(t *testing.T) {
	const r = 16000
	dir := t.TempDir()
	quiet := filepath.Join(dir, "quiet.wav")
	writeWAV(t, quiet, wavSpec{rate: r}, silence(r, 1))
	out, _ := lensRun(t, quiet)
	mustContain(t, out, "silence   all of it: no sample above -60 dBFS", "loudness: silent")
	mustLack(t, out, "waveform")

	words := filepath.Join(dir, "words.wav")
	writeWAV(t, words, wavSpec{rate: r}, cat(sine(r, 300, 0.4, -10), silence(r, 0.5), sine(r, 300, 0.4, -10), silence(r, 0.1), sine(r, 300, 0.2, -10)))
	out, _ = lensRun(t, words)
	mustContain(t, out, "a pause of 250 ms or more: 0.40-0.90 s")
	out, _ = lensRun(t, words, "--silence", "-200")
	if !strings.Contains(out, "--silence takes") {
		t.Errorf("bad --silence accepted:\n%s", out)
	}
}

func TestShortClip(t *testing.T) {
	const r = 44100
	p := filepath.Join(t.TempDir(), "tick.wav")
	writeWAV(t, p, wavSpec{rate: r}, sine(r, 2000, 0.02, -3)) // under one FFT frame
	out, code := lensRun(t, p)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "20 ms", "loudness ≈", "(under 400 ms, so over the whole clip)", "strongest at 2 kHz")
}

func TestNoise(t *testing.T) {
	const r = 32000
	x := make([]float64, r)
	seed := uint32(1)
	for i := range x {
		seed = seed*1664525 + 1013904223
		x[i] = (float64(seed>>8)/(1<<24) - 0.5) * 0.5
	}
	p := filepath.Join(t.TempDir(), "noise.wav")
	writeWAV(t, p, wavSpec{rate: r}, x)
	out, _ := lensRun(t, p)
	mustContain(t, out, "noise-like;")
	mustLack(t, out, "strongest at")
}

func TestKFilterLoudnessAt44k(t *testing.T) {
	const r = 44100
	p := filepath.Join(t.TempDir(), "st.wav")
	x := sine(r, 997, 2, -23)
	writeWAV(t, p, wavSpec{rate: r}, x, x)
	out, _ := lensRun(t, p)
	// the same signal in two channels is 3 dB louder than in one
	near64(t, "LUFS", field(t, out, `loudness (-?[\d.]+) LUFS`), -23, 0.1)
}

func TestCompressedFixtures(t *testing.T) {
	for _, c := range []struct {
		file, format string
		ffmpeg       bool
	}{
		{"testdata/tone.mp3", "MP3, 490 ms, 44.1 kHz stereo", false},
		{"testdata/mono.mp3", "MP3, 490 ms, 44.1 kHz mono", false},
		{"testdata/tone.ogg", "Ogg Vorbis, 490 ms, 44.1 kHz stereo", false},
		{"testdata/tone.opus", "Ogg Opus, 490 ms, 48 kHz stereo", true},
		{"testdata/tone.flac", "FLAC, 490 ms, 44.1 kHz stereo", true},
	} {
		t.Run(c.file, func(t *testing.T) {
			if c.ffmpeg {
				needFFmpeg(t)
			}
			out, code := lensRun(t, c.file)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			mustContain(t, out, c.format, "pitched, strongest at 880 Hz (A5)")
			// the tone starts 40 ms in: the MP3 encoder's delay is removed
			lead := field(t, out, `silence   (\d+) ms at the start`)
			near64(t, "start silence", lead, 40, 5)
			near64(t, "peak", field(t, out, `peak (-?[\d.]+) dBFS`), -21, 1.5)
		})
	}
}

func TestMP3Header(t *testing.T) {
	raw, err := os.ReadFile("testdata/tone.mp3")
	if err != nil {
		t.Fatal(err)
	}
	h := parseMP3Header(raw)
	if !h.xing || !h.lame || h.mono || h.spf != 1152 || h.delay == 0 {
		t.Errorf("header %+v", h)
	}
	raw, _ = os.ReadFile("testdata/mono.mp3")
	if h := parseMP3Header(raw); !h.mono {
		t.Errorf("mono.mp3 header %+v", h)
	}
}

func TestSoundPicture(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.wav")
	writeWAV(t, p, wavSpec{rate: 8000}, cat(silence(8000, 0.1), sine(8000, 440, 0.5, -6)))
	pic := filepath.Join(dir, "t.png")
	out, code := lensRun(t, p, "-o", pic)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	mustContain(t, out, "picture   "+pic)
	f, err := os.Open(pic)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := os.Stat(pic + ".lens-tmp"); err == nil {
		t.Error("the temporary file is left")
	}
}

func TestNoteName(t *testing.T) {
	for f, want := range map[float64]string{440: "A4", 880: "A5", 261.63: "C4", 27.5: "A0", 4186: "C8"} {
		if got := noteName(f); got != want {
			t.Errorf("noteName(%v) = %q, want %q", f, got, want)
		}
	}
}

func TestFFT(t *testing.T) {
	x := make([]complex128, 16)
	for i := range x {
		x[i] = complex(math.Cos(2*math.Pi*3*float64(i)/16), 0)
	}
	fft(x)
	for k, v := range x {
		want := 0.0
		if k == 3 || k == 13 {
			want = 8
		}
		near64(t, "bin "+strconv.Itoa(k), real(v), want, 1e-9)
	}
}
