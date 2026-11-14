package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
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

// GPT-6.1 Terra: a data chunk that ends inside a sample frame is cut short
// or malformed, not a clean end.
func TestReviewPartialFrame(t *testing.T) {
	var f bytes.Buffer
	binary.Write(&f, binary.LittleEndian, []uint16{1, 1})
	binary.Write(&f, binary.LittleEndian, []uint32{8000, 16000})
	binary.Write(&f, binary.LittleEndian, []uint16{2, 16})
	wav := func(declared uint32, data []byte) string {
		var b bytes.Buffer
		b.WriteString("RIFF")
		binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
		b.WriteString("WAVEfmt ")
		binary.Write(&b, binary.LittleEndian, uint32(16))
		b.Write(f.Bytes())
		b.WriteString("data")
		binary.Write(&b, binary.LittleEndian, declared)
		b.Write(data)
		p := filepath.Join(t.TempDir(), "p.wav")
		os.WriteFile(p, b.Bytes(), 0o644)
		return p
	}
	out, code := lensRun(t, wav(5, []byte{0, 0x40, 0, 0x40}))
	if code != 2 || !strings.Contains(out, "warning   the file ends early") {
		t.Errorf("declared 5, has 4: exit %d\n%s", code, out)
	}
	// Opus 5.5, second re-check: a stray byte past the last whole frame
	// isn't a cut file
	out, code = lensRun(t, wav(5, []byte{0, 0x40, 0, 0x40, 7}))
	if code != 0 || strings.Contains(out, "warning") {
		t.Errorf("an odd byte: exit %d\n%s", code, out)
	}
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
	mustContain(t, out, "warning   frames are missing (cut short or damaged): its header promises 490 ms of sound")
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
	k, _ := sniffFile(p)
	f, err := loadImage(p, k)
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

// Opus 5.5: color sums don't wrap on big images.
func TestReviewBigWhiteImage(t *testing.T) {
	if testing.Short() {
		t.Skip("a 17-megapixel image")
	}
	f := &imageFacts{w: 4200, h: 4200, img: canvas(4200, 4200, white)}
	analyzeImage(f)
	if got := colorsText(f); !strings.Contains(got, "#ffffff (blank)") {
		t.Errorf("colors: %s", got)
	}
}

// Opus 5.5: an RGB BMP or TIFF has no alpha channel.
func TestReviewBMPAndTIFFAlpha(t *testing.T) {
	dir := t.TempDir()
	m := opaqueRGB(canvas(16, 8, violet))
	bp := filepath.Join(dir, "rgb.bmp")
	f, _ := os.Create(bp)
	bmp.Encode(f, m)
	f.Close()
	out, _ := lensRun(t, bp)
	mustContain(t, out, "24 bits a pixel", "none: no alpha channel")
	tp := filepath.Join(dir, "gray.tif") // Go writes RGB TIFFs with alpha; gray has none
	g := image.NewGray(image.Rect(0, 0, 16, 8))
	f, _ = os.Create(tp)
	tiff.Encode(f, g, nil)
	f.Close()
	out, _ = lensRun(t, tp)
	mustContain(t, out, "TIFF, 16x8 landscape 2:1, gray 8-bit", "none: no alpha channel")
	// with real alpha, the TIFF says so
	ta := filepath.Join(dir, "rgba.tif")
	c := canvas(16, 8, clear)
	rect(c, image.Rect(2, 2, 6, 6), violet)
	f, _ = os.Create(ta)
	tiff.Encode(f, c, nil)
	f.Close()
	out, _ = lensRun(t, ta)
	mustContain(t, out, "RGBA 8-bit", "real transparent background")
}

// Opus 5.5: 8-bit PCM's positive full scale is 127/128.
func TestReviewEightBitClipping(t *testing.T) {
	x := sine(8000, 200, 0.5, 6)
	for i, v := range x {
		x[i] = max(v, -0.5) // clipped on the positive side only
	}
	p := filepath.Join(t.TempDir(), "pos8.wav")
	writeWAV(t, p, wavSpec{rate: 8000, bits: 8}, x)
	out, _ := lensRun(t, p)
	mustContain(t, out, "clipped: ", "in 100 runs")
}

// Opus 5.5: a big ID3 tag (cover art) doesn't hide the first frame, and
// the bitrate leaves the tag out.
func TestReviewBigID3Tag(t *testing.T) {
	raw, err := os.ReadFile("testdata/mono.mp3")
	if err != nil {
		t.Fatal(err)
	}
	const pad = 200000
	tag := []byte{'I', 'D', '3', 4, 0, 0, byte(pad >> 21 & 0x7f), byte(pad >> 14 & 0x7f), byte(pad >> 7 & 0x7f), byte(pad & 0x7f)}
	tag = append(tag, make([]byte, pad)...)
	start := id3Size(raw)
	p := filepath.Join(t.TempDir(), "art.mp3")
	os.WriteFile(p, append(tag, raw[start:]...), 0o644)
	out, code := lensRun(t, p)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	mustContain(t, out, "MP3, 490 ms, 44.1 kHz mono", "40 ms at the start")
	mustLack(t, out, "kb/s)") // under half a second: no bitrate
	// a FLAC with an ID3 tag in front is a FLAC
	fl, _ := os.ReadFile("testdata/tone.flac")
	fp := filepath.Join(t.TempDir(), "tagged.flac")
	os.WriteFile(fp, append(append([]byte(nil), tag...), fl...), 0o644)
	if k, _ := sniffFile(fp); k != kFLAC {
		t.Errorf("FLAC behind an ID3 tag is %v", k)
	}
}

// Opus 5.5: damaged or cut Ogg and FLAC files say so.
func TestReviewDamagedOgg(t *testing.T) {
	raw, err := os.ReadFile("testdata/tone.ogg")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bad := append([]byte(nil), raw...)
	for i := len(bad) - 200; i < len(bad)-150; i++ {
		bad[i] ^= 0x5a
	}
	bp := filepath.Join(dir, "bad.ogg")
	os.WriteFile(bp, bad, 0o644)
	out, code := lensRun(t, bp)
	if code != 2 || !strings.Contains(out, "fails its checksum") {
		t.Errorf("damaged Ogg: exit %d\n%s", code, out)
	}
	if oggCRC(raw[:58]) == 0 { // a sanity check that the first page is real
		t.Error("no checksum")
	}
	if _, problem := oggPages("testdata/tone.ogg"); problem != "" {
		t.Errorf("a good Ogg: %s", problem)
	}
}

func TestReviewChainedOggAndFLACDamage(t *testing.T) {
	needFFmpeg(t)
	raw, _ := os.ReadFile("testdata/tone.ogg")
	dir := t.TempDir()
	chain := filepath.Join(dir, "chain.ogg")
	os.WriteFile(chain, append(append([]byte(nil), raw...), raw...), 0o644)
	out, _ := lensRun(t, chain)
	near64(t, "chained length", field(t, out, `Ogg Vorbis, (\d+) ms`), 985, 25)
	fl, _ := os.ReadFile("testdata/tone.flac")
	for _, c := range []struct{ name, codec, want string }{
		{"long.flac", "flac", "warning   ffmpeg reports a problem: Decoding error"},
		{"long.ogg", "libvorbis", "warning   the file ends early: it stops inside an Ogg page"},
	} {
		full := filepath.Join(dir, c.name)
		if err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:a", c.codec, full).Run(); err != nil {
			t.Skip("ffmpeg can't make the file: ", err)
		}
		raw, _ := os.ReadFile(full)
		cut := filepath.Join(dir, "cut-"+c.name)
		os.WriteFile(cut, raw[:len(raw)*2/3], 0o644)
		out, code := lensRun(t, cut)
		if code != 2 || !strings.Contains(out, c.want) {
			t.Errorf("cut %s: exit %d\n%s", c.name, code, out)
		}
	}
	// a colon in the name isn't a protocol
	colon := filepath.Join(dir, "12:30 take.flac")
	os.WriteFile(colon, fl, 0o644)
	out, code := lensRun(t, colon)
	if code != 0 {
		t.Errorf("colon name: exit %d\n%s", code, out)
	}
}

func TestReviewFFmpegMadeFormats(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	alaw := filepath.Join(dir, "alaw.wav")
	m25 := filepath.Join(dir, "m25.mp3")
	for _, c := range [][]string{
		{"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5", "-c:a", "pcm_alaw", alaw},
		{"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5:sample_rate=8000", "-c:a", "libmp3lame", m25},
	} {
		if err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, c...)...).Run(); err != nil {
			t.Skip("ffmpeg can't make the file: ", err)
		}
	}
	out, _ := lensRun(t, alaw)
	mustContain(t, out, "WAV A-law, 500 ms")
	out, code := lensRun(t, m25)
	if code != 0 {
		t.Fatalf("MPEG 2.5: exit %d\n%s", code, out)
	}
	mustContain(t, out, "MP3", "8 kHz mono")
}

// Opus 5.5: frames after the hop grew count for the stretch they stand for.
func TestReviewToneWeighting(t *testing.T) {
	const r = 16000
	a := sine(r, 440, 20, -12)
	b := sine(r, 6000, 60, -12)
	dir := t.TempDir()
	p1, p2 := filepath.Join(dir, "1.wav"), filepath.Join(dir, "2.wav")
	writeWAV(t, p1, wavSpec{rate: r}, cat(a, b))
	writeWAV(t, p2, wavSpec{rate: r}, cat(b, a))
	share := `above (\d+)%`
	h1 := field(t, mustOut(t, p1), share)
	h2 := field(t, mustOut(t, p2), share)
	near64(t, "high share, tone first", h1, 75, 3)
	near64(t, "high share, tone last", h2, 75, 3)
}

func mustOut(t *testing.T, args ...string) string {
	t.Helper()
	out, code := lensRun(t, args...)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	return out
}

// Opus 5.5: a click shorter than one FFT frame is analyzed in the middle
// of the window, not at its tapered end.
func TestReviewTinyClick(t *testing.T) {
	p := filepath.Join(t.TempDir(), "click.wav")
	writeWAV(t, p, wavSpec{rate: 44100}, sine(44100, 2000, 0.005, -6))
	out := mustOut(t, p)
	mustLack(t, out, "noise-like")
	one := filepath.Join(t.TempDir(), "one.wav")
	writeWAV(t, one, wavSpec{rate: 44100}, []float64{0.5})
	out = mustOut(t, one)
	mustLack(t, out, "energy under 250 Hz 0%, 250 Hz-4 kHz 0%")
}

// Opus 5.5: BS.1770 isn't defined at very low sample rates.
func TestReviewLowRateLoudness(t *testing.T) {
	p := filepath.Join(t.TempDir(), "low.wav")
	writeWAV(t, p, wavSpec{rate: 2000}, sine(2000, 100, 3, -18))
	out := mustOut(t, p)
	mustContain(t, out, "loudness not measured")
}

// Opus 5.5: small output slips.
func TestReviewOutputSlips(t *testing.T) {
	if got := durText(secDur(119.97)); got != "2:00.0" {
		t.Errorf("durText(119.97 s) = %q", got)
	}
	if got := durText(secDur(3599.97)); got != "1:00:00" {
		t.Errorf("durText(3599.97 s) = %q", got)
	}
	if k := sniff([]byte("\x00\x00\x00\x1cftypavif"), "x.avif"); k != kHEIF {
		t.Errorf("AVIF is %v", k)
	}
	dir := t.TempDir()
	a := canvas(1000, 1000, white)
	b := canvas(1000, 1000, white)
	for i := 0; i < 5; i++ {
		rect(b, image.Rect(50+i*190, 50+i*190, 60+i*190, 60+i*190), black)
	}
	pa := savePNG(t, dir, "shot.png", opaqueRGB(a))
	sub := filepath.Join(dir, "x")
	os.Mkdir(sub, 0o755)
	f, _ := os.Create(filepath.Join(dir, "shot.jpg"))
	jpeg.Encode(f, b, &jpeg.Options{Quality: 100})
	f.Close()
	out, _ := lensRun(t, "diff", pa, filepath.Join(dir, "shot.jpg"), "--fuzz", "20")
	mustContain(t, out, "shot-before-crop1.png", "shot-after-crop1.png", "  spot 1:")
	out, _ = lensRun(t, "diff", pa, filepath.Join(dir, "shot.jpg"), "--fuzz", "20", "-b", "12")
	mustContain(t, out, "more crops (-b 0 shows them)")
	if n := strings.Count(out, "\n"); n > 13 {
		t.Errorf("%d lines for -b 12:\n%s", n, out)
	}
}

// Opus 5.5: an image too big to decode is refused before it eats the memory.
func TestReviewHugeImageRefused(t *testing.T) {
	var ihdr bytes.Buffer
	binary.Write(&ihdr, binary.BigEndian, uint32(20000))
	binary.Write(&ihdr, binary.BigEndian, uint32(20000))
	ihdr.Write([]byte{8, 6, 0, 0, 0})
	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	binary.Write(&out, binary.BigEndian, uint32(13))
	c := crc32.NewIEEE()
	c.Write([]byte("IHDR"))
	c.Write(ihdr.Bytes())
	out.WriteString("IHDR")
	out.Write(ihdr.Bytes())
	binary.Write(&out, binary.BigEndian, c.Sum32())
	p := filepath.Join(t.TempDir(), "huge.png")
	os.WriteFile(p, out.Bytes(), 0o644)
	got, code := lensRun(t, p)
	if code != 2 || !strings.Contains(got, "lens doesn't decode images this big") {
		t.Errorf("exit %d: %s", code, got)
	}
}

// Sonnet 5.5: MPEG-2 MP3s go through ffmpeg, which decodes them right.
func TestReviewMPEG2MP3(t *testing.T) {
	needFFmpeg(t)
	p := filepath.Join(t.TempDir(), "q.mp3")
	if err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=1:sample_rate=22050",
		"-af", "volume=-10dB", "-ac", "1", "-c:a", "libmp3lame", "-b:a", "32k", p).Run(); err != nil {
		t.Skip("ffmpeg can't make the file: ", err)
	}
	out := mustOut(t, p)
	near64(t, "peak", field(t, out, `peak (-?[\d.]+) dBFS`), -28.4, 1)
	mustContain(t, out, "pitched, strongest at 440 Hz")
}

// Sonnet 5.5: absurd header sizes are refused, not allocated.
func TestReviewAbsurdWAVHeaders(t *testing.T) {
	dir := t.TempDir()
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(0xfffffff0))
	b.Write(make([]byte, 32))
	p := filepath.Join(dir, "hugefmt.wav")
	os.WriteFile(p, b.Bytes(), 0o644)
	out, code := lensRun(t, p)
	if code != 2 || !strings.Contains(out, "broken fmt chunk") {
		t.Errorf("huge fmt: exit %d %s", code, out)
	}
	many := filepath.Join(dir, "many.wav")
	chans := make([][]float64, 65)
	for i := range chans {
		chans[i] = []float64{0, 0.1}
	}
	writeWAV(t, many, wavSpec{rate: 8000}, chans...)
	out, code = lensRun(t, many)
	if code != 2 || !strings.Contains(out, "65 channels") {
		t.Errorf("65 channels: exit %d %s", code, out)
	}
}

// Sonnet 5.5: a one-color shape on a clear background isn't blank, and four
// clear corners aren't a transparent background.
func TestReviewBlankAndCorners(t *testing.T) {
	dir := t.TempDir()
	m := canvas(100, 100, clear)
	rect(m, image.Rect(20, 20, 80, 80), color.NRGBA{255, 0, 0, 255})
	out, _ := lensRun(t, savePNG(t, dir, "shape.png", m))
	mustContain(t, out, "one: every visible pixel is #ff0000")
	mustLack(t, out, "blank")
	c := canvas(100, 100, color.NRGBA{0, 0, 255, 255})
	for _, p := range [][2]int{{0, 0}, {99, 0}, {0, 99}, {99, 99}} {
		c.SetNRGBA(p[0], p[1], clear)
	}
	out, _ = lensRun(t, savePNG(t, dir, "corners.png", c))
	mustContain(t, out, "only the corners are clear (rounded corners?)")
	mustLack(t, out, "real transparent background")
	if got := pct(996, 1000); got != "99.6%" {
		t.Errorf("pct(996, 1000) = %q", got)
	}
}

// Sonnet 5.5: samples that aren't numbers are counted, not let loose.
func TestReviewNaNSamples(t *testing.T) {
	x := make([]float64, 400)
	for i := range x {
		x[i] = []float64{0.5, math.NaN(), 0.25, math.Inf(1)}[i%4]
	}
	p := filepath.Join(t.TempDir(), "nan.wav")
	writeWAV(t, p, wavSpec{rate: 8000, bits: 32, float: true}, x)
	out := mustOut(t, p)
	mustContain(t, out, "200 samples weren't numbers (NaN or infinite), counted as 0", "peak -6.0 dBFS")
	mustLack(t, out, "NaN dBFS", "all of it")
}

// Sonnet 5.5: channels in opposite phase still have a tone and a spectrum.
func TestReviewOppositePhaseTone(t *testing.T) {
	a := sine(16000, 440, 1, -12)
	neg := make([]float64, len(a))
	for i, v := range a {
		neg[i] = -v
	}
	p := filepath.Join(t.TempDir(), "anti.wav")
	writeWAV(t, p, wavSpec{rate: 16000}, a, neg)
	out := mustOut(t, p)
	mustContain(t, out, "opposite phase", "pitched, strongest at 440 Hz (A4)", "█")
}

// Sonnet 5.5: loudness weights follow the speaker layout.
func TestReviewChannelWeights(t *testing.T) {
	got := channelWeights(8, 0, "")
	want := []float64{1, 1, 1, 0, 1.41, 1.41, 1.41, 1.41}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("7.1 weights %v, want %v", got, want)
		}
	}
	if w := channelWeights(6, 0x60f, ""); w[3] != 0 || w[4] != 1.41 { // 5.1 with side channels
		t.Errorf("5.1(side) weights %v", w)
	}
	// GPT-6.1 Terra: ffmpeg's layout for what it decodes counts too
	if w := channelWeights(3, 0, "2.1"); w[2] != 0 || w[0] != 1 {
		t.Errorf("2.1 weights %v", w)
	}
	if w := channelWeights(3, 0, "FL+FR+LFE"); w[2] != 0 {
		t.Errorf("FL+FR+LFE weights %v", w)
	}
	if w := channelWeights(3, 0, "3.0"); w[2] != 1 {
		t.Errorf("3.0 weights %v", w)
	}
	// a tone in the LFE alone has no loudness
	const r = 8000
	chans := make([][]float64, 8)
	for i := range chans {
		chans[i] = silence(r, 1)
	}
	chans[3] = sine(r, 60, 1, -6)
	p := filepath.Join(t.TempDir(), "lfe.wav")
	writeWAV(t, p, wavSpec{rate: r}, chans...)
	out, _ := lensRun(t, p)
	mustContain(t, out, "loudness: silent")
}

// Sonnet 5.5: errors say what's wrong, and nothing to show is a failure.
func TestReviewErrorWords(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	os.Mkdir(empty, 0o755)
	if _, code := lensRun(t, empty); code != 2 {
		t.Errorf("an empty folder exits %d", code)
	}
	pa := savePNG(t, dir, "a.png", opaqueRGB(canvas(10, 10, white)))
	out, code := lensRun(t, "diff", empty, pa)
	if code != 2 || !strings.Contains(out, "is a folder") {
		t.Errorf("diff with a folder: %d %s", code, out)
	}
	pb := savePNG(t, dir, "b.png", opaqueRGB(canvas(10, 5, white)))
	pic := filepath.Join(dir, "d.png")
	out, _ = lensRun(t, "diff", pa, pb, "-o", pic)
	mustContain(t, out, "in the shared 10x5 area, every pixel is the same; the sizes differ", "picture  not written: nothing changed")
	if os.Getuid() != 0 {
		locked := savePNG(t, dir, "locked.png", opaqueRGB(canvas(4, 4, white)))
		os.Chmod(locked, 0)
		out, code = lensRun(t, locked)
		if code != 2 || !strings.Contains(out, "permission denied") {
			t.Errorf("unreadable: %d %s", code, out)
		}
	}
	for s, want := range map[float64]string{0.9996: "1.00 s", 59.97: "1:00.0", 9.997: "10.0 s"} {
		if got := secText(s); got != want {
			t.Errorf("secText(%v) = %q, want %q", s, got, want)
		}
	}
	if got := dbText(32767.0 / 32768); got != "0.0 dBFS" {
		t.Errorf("dbText(full scale) = %q", got)
	}
}

// Opus 5.5, re-check: chained Ogg is clean, a flipped FLAC byte and cut
// files on the ffmpeg path warn.
func TestRecheckFFmpegPaths(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	mk := func(name string, args ...string) string {
		p := filepath.Join(dir, name)
		all := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=3"}, args...)
		if err := exec.Command("ffmpeg", append(all, p)...).Run(); err != nil {
			t.Skip("ffmpeg can't make ", name, ": ", err)
		}
		return p
	}
	a := mk("a.ogg", "-c:a", "libvorbis")
	b := mk("b.ogg", "-c:a", "libvorbis", "-q:a", "8")
	ra, _ := os.ReadFile(a)
	rb, _ := os.ReadFile(b)
	chain := filepath.Join(dir, "chain.ogg")
	os.WriteFile(chain, append(ra, rb...), 0o644)
	out, code := lensRun(t, chain)
	if code != 0 || strings.Contains(out, "warning") {
		t.Errorf("chained Ogg: exit %d\n%s", code, out)
	}
	mustContain(t, out, "6.00 s")

	fl, _ := os.ReadFile(mk("f.flac", "-c:a", "flac"))
	fl[len(fl)*2/3] ^= 0xff
	flipped := filepath.Join(dir, "flipped.flac")
	os.WriteFile(flipped, fl, 0o644)
	if out, code := lensRun(t, flipped); code != 2 || !strings.Contains(out, "warning   ffmpeg reports a problem") {
		t.Errorf("flipped FLAC byte: exit %d\n%s", code, out)
	}

	for _, c := range []struct{ name string }{{"alaw.wav"}, {"m2.mp3"}} {
		var p string
		if c.name == "alaw.wav" {
			p = mk(c.name, "-c:a", "pcm_alaw")
		} else {
			p = filepath.Join(dir, c.name)
			if err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=3:sample_rate=22050",
				"-c:a", "libmp3lame", "-b:a", "32k", p).Run(); err != nil {
				t.Skip(err)
			}
		}
		raw, _ := os.ReadFile(p)
		cut := filepath.Join(dir, "cut-"+c.name)
		os.WriteFile(cut, raw[:len(raw)/2], 0o644)
		want := "warning   the file ends early: its header promises 3.00 s"
		if c.name == "alaw.wav" { // a WAV: by its bytes, not its byte rate
			want = "warning   the file ends early: it holds "
		}
		out, code := lensRun(t, cut)
		if code != 2 || !strings.Contains(out, want) {
			t.Errorf("cut %s: exit %d\n%s", c.name, code, out)
		}
	}
}

// Opus 5.5, re-check: a data size left at 0 or at a pipe's placeholder is
// read to the end of the file, without a warning.
func TestRecheckWAVPlaceholders(t *testing.T) {
	x := sine(8000, 440, 2, -10)
	for _, size := range []int64{0, 0x80000000, 0x7ffff000} {
		p := filepath.Join(t.TempDir(), "p.wav")
		writeWAV(t, p, wavSpec{rate: 8000, dataSize: size}, x)
		raw, _ := os.ReadFile(p)
		if size == 0 { // a real RIFF size, as a killed writer leaves it
			binary.LittleEndian.PutUint32(raw[4:8], 36)
			os.WriteFile(p, raw, 0o644)
		}
		out, code := lensRun(t, p)
		if code != 0 || !strings.Contains(out, "2.00 s, 8 kHz mono") {
			t.Errorf("data size %#x: exit %d\n%s", size, code, out)
		}
	}
}

// Opus 5.5, re-check: wording for mostly clear borders, crop names that
// collide or overwrite an input, and ffmpeg's guess for 3 channels.
func TestRecheckWordsAndNames(t *testing.T) {
	dir := t.TempDir()
	m := canvas(100, 100, clear)
	rect(m, image.Rect(0, 20, 51, 71), violet)
	out, _ := lensRun(t, savePNG(t, dir, "touch.png", m))
	mustContain(t, out, "of the border is clear")
	mustLack(t, out, "only the corners")

	if st := cropStems("shot.jpg", "./shot.png", 1, false); st[0] == "" {
		t.Errorf("shot.jpg and ./shot.png: stems %q", st)
	}
	if st := cropStems("x.png", "x-crop.png", 1, false); st[0] == "" {
		t.Errorf("x.png and x-crop.png: stems %q", st)
	}
	if st := cropStems("a.png", "b.png", 3, true); st[0] != "" {
		t.Errorf("a.png and b.png: stems %q", st)
	}
	if w := channelWeights(3, 0, ""); w[2] != 0 {
		t.Errorf("3 channels without a mask: %v, want ffmpeg's 2.1", w)
	}
}

// Sonnet 5.5, re-check: no "-0.0", no overflowing pads or times.
func TestRecheckSonnetSlips(t *testing.T) {
	if got := oneDecimal(-0.04); got != "0.0" {
		t.Errorf("oneDecimal(-0.04) = %q", got)
	}
	if got := secText(4.6e18); !strings.Contains(got, "e+18 s") {
		t.Errorf("secText(4.6e18) = %q", got)
	}
	dir := t.TempDir()
	a := canvas(50, 40, white)
	b := canvas(50, 40, white)
	rect(b, image.Rect(10, 10, 20, 20), black)
	out, _ := lensRun(t, "diff", "--pad", "9223372036854775807", savePNG(t, dir, "a.png", opaqueRGB(a)), savePNG(t, dir, "b.png", opaqueRGB(b)))
	mustContain(t, out, "crop     50x40+0+0 for both")
	// RF64 with an impossible ds64 size reads what's there
	plain := filepath.Join(dir, "plain.wav")
	writeWAV(t, plain, wavSpec{rate: 8000}, sine(8000, 440, 1, -10))
	raw, _ := os.ReadFile(plain)
	fmtAt, dataAt := bytes.Index(raw, []byte("fmt ")), bytes.Index(raw, []byte("data"))
	var rf bytes.Buffer
	rf.WriteString("RF64\xff\xff\xff\xffWAVEds64")
	binary.Write(&rf, binary.LittleEndian, uint32(28))
	binary.Write(&rf, binary.LittleEndian, []uint64{0, 1 << 62, 0})
	binary.Write(&rf, binary.LittleEndian, uint32(0))
	rf.Write(raw[fmtAt:dataAt])
	rf.WriteString("data\xff\xff\xff\xff")
	rf.Write(raw[dataAt+8:])
	p := filepath.Join(dir, "rf.wav")
	os.WriteFile(p, rf.Bytes(), 0o644)
	out, code := lensRun(t, p)
	if code != 0 || !strings.Contains(out, "1.00 s, 8 kHz mono") {
		t.Errorf("RF64 with 2^62: exit %d\n%s", code, out)
	}
	// short by less than the precision shown
	short := filepath.Join(dir, "short.wav")
	writeWAV(t, short, wavSpec{rate: 8000}, sine(8000, 440, 3, -10))
	raw, _ = os.ReadFile(short)
	os.WriteFile(short, raw[:len(raw)-4], 0o644)
	out, _ = lensRun(t, short)
	mustContain(t, out, "warning   the file ends early: 4 bytes short of the data its header promises")
}

// GPT-6.1 Terra, second re-check: a chained Ogg whose first stream lost its
// last page says so, though the last stream ends fine.
func TestRecheck2ChainedStreamCut(t *testing.T) {
	raw, err := os.ReadFile("testdata/tone.ogg")
	if err != nil {
		t.Fatal(err)
	}
	var offs []int
	for i := 0; i+27 <= len(raw); {
		offs = append(offs, i)
		n := int(raw[i+26])
		body := 0
		for _, s := range raw[i+27 : i+27+n] {
			body += int(s)
		}
		i += 27 + n + body
	}
	p := filepath.Join(t.TempDir(), "chain.ogg")
	os.WriteFile(p, append(append([]byte(nil), raw[:offs[len(offs)-1]]...), raw...), 0o644)
	// this fixture's sound is all in its last page, so the first link has
	// none left: an error, with the damage named first
	out, code := lensRun(t, p)
	if code != 2 || !strings.Contains(out, "the file is damaged: an Ogg stream lacks its last page where the next one starts") {
		t.Errorf("exit %d\n%s", code, out)
	}
	clean := filepath.Join(t.TempDir(), "clean.ogg")
	os.WriteFile(clean, append(append([]byte(nil), raw...), raw...), 0o644)
	if out, code := lensRun(t, clean); code != 0 || strings.Contains(out, "warning") {
		t.Errorf("a clean chain: exit %d\n%s", code, out)
	}
}

func TestRecheck2ChainedStreamCutDecodes(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.ogg"), filepath.Join(dir, "b.ogg")
	for p, f := range map[string]string{a: "sine=frequency=440:duration=5", b: "sine=frequency=880:duration=1"} {
		if err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", f, "-c:a", "libvorbis", p).Run(); err != nil {
			t.Skip(err)
		}
	}
	ra, _ := os.ReadFile(a)
	rb, _ := os.ReadFile(b)
	last := 0
	for i := 0; i+27 <= len(ra); {
		last = i
		n := int(ra[i+26])
		body := 0
		for _, s := range ra[i+27 : i+27+n] {
			body += int(s)
		}
		i += 27 + n + body
	}
	p := filepath.Join(dir, "chain.ogg")
	os.WriteFile(p, append(append([]byte(nil), ra[:last]...), rb...), 0o644)
	out, code := lensRun(t, p)
	if code != 2 || !strings.Contains(out, "warning   the file is damaged: an Ogg stream lacks its last page") || !strings.Contains(out, "Ogg Vorbis, ") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

// Opus 5.5, second re-check.
func TestRecheck2WAVSizes(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.wav")
	writeWAV(t, plain, wavSpec{rate: 8000}, sine(8000, 440, 1, -10))
	raw, _ := os.ReadFile(plain)
	fmtAt, dataAt := bytes.Index(raw, []byte("fmt ")), bytes.Index(raw, []byte("data"))
	// an RF64 written to a pipe: every size a placeholder, ds64 all 0
	var rf bytes.Buffer
	rf.WriteString("RF64\xff\xff\xff\xffWAVEds64")
	binary.Write(&rf, binary.LittleEndian, uint32(28))
	binary.Write(&rf, binary.LittleEndian, []uint64{0, 0, 0})
	binary.Write(&rf, binary.LittleEndian, uint32(0))
	rf.Write(raw[fmtAt:dataAt])
	rf.WriteString("data\xff\xff\xff\xff")
	rf.Write(raw[dataAt+8:])
	p := filepath.Join(dir, "rfpipe.wav")
	os.WriteFile(p, rf.Bytes(), 0o644)
	if out, code := lensRun(t, p); code != 0 || !strings.Contains(out, "1.00 s, 8 kHz mono") {
		t.Errorf("RF64 from a pipe: exit %d\n%s", code, out)
	}
	// data size 0, then sound that happens to start with letters
	x := make([]float64, 8000)
	for i := range x {
		x[i] = float64(0x4142) / 32768
	}
	z := filepath.Join(dir, "zero.wav")
	writeWAV(t, z, wavSpec{rate: 8000, dataSize: 0}, x)
	zr, _ := os.ReadFile(z)
	binary.LittleEndian.PutUint32(zr[4:8], 36) // a real RIFF size
	binary.LittleEndian.PutUint32(zr[bytes.Index(zr, []byte("data"))+4:], 0)
	os.WriteFile(z, zr, 0o644)
	if out, code := lensRun(t, z); code != 0 || !strings.Contains(out, "1.00 s") {
		t.Errorf("size 0, letters after: exit %d\n%s", code, out)
	}
	// short by 3 bytes says 3
	short := filepath.Join(dir, "short.wav")
	writeWAV(t, short, wavSpec{rate: 8000}, sine(8000, 440, 3, -10))
	sr, _ := os.ReadFile(short)
	os.WriteFile(short, sr[:len(sr)-3], 0o644)
	out, _ := lensRun(t, short)
	mustContain(t, out, "3 bytes short of the data its header promises")
}

func TestRecheck2XingWithoutLAME(t *testing.T) {
	raw, err := os.ReadFile("testdata/tone.mp3")
	if err != nil {
		t.Fatal(err)
	}
	b := append([]byte(nil), raw...)
	for _, tag := range []string{"LAME", "Lavc", "Lavf"} {
		for i := bytes.Index(b[:4096], []byte(tag)); i >= 0; i = bytes.Index(b[:4096], []byte(tag)) {
			copy(b[i:], []byte{0, 0, 0, 0})
		}
	}
	dir := t.TempDir()
	whole := filepath.Join(dir, "nolame.mp3")
	os.WriteFile(whole, b, 0o644)
	if out, code := lensRun(t, whole); code != 0 || strings.Contains(out, "warning") {
		t.Errorf("whole file without a LAME tag: exit %d\n%s", code, out)
	}
	cut := filepath.Join(dir, "cut.mp3")
	os.WriteFile(cut, b[:len(b)*2/3], 0o644)
	if out, code := lensRun(t, cut); code != 2 || !strings.Contains(out, "frames are missing") {
		t.Errorf("cut without a LAME tag: exit %d\n%s", code, out)
	}
}

func TestRecheck2Corners(t *testing.T) {
	dir := t.TempDir()
	m := canvas(200, 200, color.NRGBA{51, 102, 153, 255})
	// clear corners of radius 50
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			cx, cy := min(max(x, 50), 149), min(max(y, 50), 149)
			if (x-cx)*(x-cx)+(y-cy)*(y-cy) > 50*50 {
				m.SetNRGBA(x, y, clear)
			}
		}
	}
	out, _ := lensRun(t, savePNG(t, dir, "round.png", m))
	mustContain(t, out, "only the corners are clear (rounded corners?)")
	if w := channelWeights(12, 0, ""); w[3] != 0 || w[4] != 1.41 {
		t.Errorf("12 channels, ffmpeg's 7.1.4: %v", w)
	}
	for _, v := range channelWeights(16, 0, "flat") {
		if v != 1 {
			t.Fatalf("Opus without a layout weighs every channel 1")
		}
	}
}

func TestRecheck2TableWarnings(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 12; i++ {
		p := filepath.Join(dir, fmt.Sprintf("cut%02d.wav", i))
		writeWAV(t, p, wavSpec{rate: 8000}, sine(8000, 440, 0.5, -10))
		r, _ := os.ReadFile(p)
		os.WriteFile(p, r[:len(r)-100], 0o644)
	}
	out, code := lensRun(t, dir, "-b", "12")
	if code != 2 || !strings.Contains(out, "more warnings") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if n := strings.Count(out, "\n"); n > 16 {
		t.Errorf("%d lines for -b 12:\n%s", n, out)
	}
}

func TestRecheck2FFmpegFiles(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	run := func(args ...string) bool {
		return exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).Run() == nil
	}
	y := filepath.Join(dir, "y.wav")
	if run("-f", "lavfi", "-i", "sine=d=1.37:r=44100", "-c:a", "adpcm_yamaha", y) {
		if out, code := lensRun(t, y); code != 0 || strings.Contains(out, "warning") {
			t.Errorf("a whole Yamaha ADPCM WAV: exit %d\n%s", code, out)
		}
	}
	m2 := filepath.Join(dir, "m2.mp3")
	if run("-f", "lavfi", "-i", "sine=f=440:d=60:sample_rate=22050", "-c:a", "libmp3lame", "-b:a", "32k", m2) {
		r, _ := os.ReadFile(m2)
		cut := filepath.Join(dir, "m2cut.mp3")
		os.WriteFile(cut, r[:len(r)-3000], 0o644)
		if out, code := lensRun(t, cut); code != 2 || !strings.Contains(out, "the file ends early") {
			t.Errorf("an MPEG-2 MP3 short by under 1%%: exit %d\n%s", code, out)
		}
	}
	a, cover, art := filepath.Join(dir, "a.m4a"), filepath.Join(dir, "cover.png"), filepath.Join(dir, "art.m4a")
	big := canvas(600, 600, white)
	for i := range big.Pix {
		big.Pix[i] = byte(i * 7919 % 251) // noise: a picture that doesn't compress
	}
	savePNG(t, dir, "cover.png", big)
	if run("-f", "lavfi", "-i", "sine=d=2", "-c:a", "aac", "-b:a", "128k", a) && run("-i", a, "-i", cover, "-map", "0", "-map", "1", "-c", "copy", "-disposition:v", "attached_pic", art) {
		out, _ := lensRun(t, art)
		kbs := field(t, out, `\(~(\d+) kb/s\)`)
		near64(t, "bitrate with cover art", kbs, 125, 25)
	}
}
