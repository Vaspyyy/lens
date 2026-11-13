package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffTwoSpots(t *testing.T) {
	dir := t.TempDir()
	bg := color.NRGBA{30, 30, 46, 255}
	a := canvas(1000, 700, bg)
	rect(a, image.Rect(800, 30, 900, 60), color.NRGBA{137, 180, 250, 255})
	b := canvas(1000, 700, bg)
	rect(b, image.Rect(800, 30, 920, 60), color.NRGBA{243, 139, 168, 255})
	rect(b, image.Rect(40, 600, 80, 640), white)
	pa := savePNG(t, dir, "before.png", opaqueRGB(a))
	pb := savePNG(t, dir, "after.png", opaqueRGB(b))
	out, code := lensRun(t, "diff", pa, pb)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "1000x700 both", "in 2 spots",
		"spot 1  120x30+800+30", "spot 2  40x40+40+600",
		"crop     one per spot",
		"magick "+pa+" -crop 152x62+784+14 +repage "+filepath.Join(dir, "before-crop1.png"),
		"magick "+pb+" -crop 152x62+784+14 +repage "+filepath.Join(dir, "after-crop1.png"),
		"-crop 72x72+24+584 +repage")
}

func TestDiffOneSpotPadClamped(t *testing.T) {
	dir := t.TempDir()
	a := canvas(200, 100, white)
	b := canvas(200, 100, white)
	rect(b, image.Rect(0, 0, 10, 10), black)
	pa := savePNG(t, dir, "a.png", opaqueRGB(a))
	pb := savePNG(t, dir, "b.png", opaqueRGB(b))
	out, _ := lensRun(t, "diff", pa, pb, "--pad", "8")
	mustContain(t, out, "100 pixels (0.5%) in 1 spot", "crop     18x18+0+0 for both (the changes plus 8 px", "-crop 18x18+0+0 +repage "+filepath.Join(dir, "a-crop.png"))
}

func TestDiffSame(t *testing.T) {
	dir := t.TempDir()
	m := canvas(20, 20, violet)
	pa := savePNG(t, dir, "a.png", opaqueRGB(m))
	out, _ := lensRun(t, "diff", pa, pa)
	mustContain(t, out, "byte for byte")
	// the same pixels in another encoding
	f, _ := os.Create(filepath.Join(dir, "b.png"))
	(&png.Encoder{CompressionLevel: png.NoCompression}).Encode(f, opaqueRGB(m))
	f.Close()
	out, _ = lensRun(t, "diff", pa, filepath.Join(dir, "b.png"))
	mustContain(t, out, "every pixel is the same")
}

func TestDiffClearPixelsAreEqual(t *testing.T) {
	dir := t.TempDir()
	a := canvas(10, 10, color.NRGBA{255, 0, 0, 0})
	b := canvas(10, 10, color.NRGBA{0, 255, 0, 0})
	a.SetNRGBA(5, 5, violet)
	b.SetNRGBA(5, 5, violet)
	out, _ := lensRun(t, "diff", savePNG(t, dir, "a.png", a), savePNG(t, dir, "b.png", b))
	mustContain(t, out, "every pixel is the same")
}

func TestDiffFuzzAndSizes(t *testing.T) {
	dir := t.TempDir()
	a := canvas(50, 40, color.NRGBA{100, 100, 100, 255})
	b := canvas(60, 40, color.NRGBA{100, 100, 100, 255})
	rect(b, image.Rect(10, 10, 20, 20), color.NRGBA{104, 100, 100, 255})
	pa := savePNG(t, dir, "a.png", opaqueRGB(a))
	pb := savePNG(t, dir, "b.png", opaqueRGB(b))
	out, _ := lensRun(t, "diff", pa, pb)
	mustContain(t, out, "50x40 and 60x40: the sizes differ, so the 50x40 at the top left is compared", "100 pixels")
	out, _ = lensRun(t, "diff", pa, pb, "--fuzz", "2")
	mustContain(t, out, "no pixel differs by more than 2%")
}

func TestDiffPicture(t *testing.T) {
	dir := t.TempDir()
	a := canvas(100, 80, white)
	b := canvas(100, 80, white)
	rect(b, image.Rect(30, 30, 50, 40), black)
	pic := filepath.Join(dir, "d.png")
	out, code := lensRun(t, "diff", savePNG(t, dir, "a.png", opaqueRGB(a)), savePNG(t, dir, "b.png", opaqueRGB(b)), "-o", pic)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	f, err := os.Open(pic)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	// the crop is 52x42 (20x10 plus 16 px each side); three panels and two gaps
	if cfg.Width != 3*52+16 || cfg.Height != 42 {
		t.Errorf("picture is %dx%d", cfg.Width, cfg.Height)
	}
}

func TestDiffRefusesSounds(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.wav")
	writeWAV(t, p, wavSpec{}, sine(44100, 440, 0.1, -6))
	out, code := lensRun(t, "diff", p, p)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "is a sound; lens diff compares images")
}

func TestFolderTable(t *testing.T) {
	dir := t.TempDir()
	icon := canvas(40, 40, clear)
	rect(icon, image.Rect(10, 10, 30, 30), violet)
	savePNG(t, dir, "icon.png", icon)
	savePNG(t, dir, "bg.png", opaqueRGB(canvas(64, 36, black)))
	writeWAV(t, filepath.Join(dir, "beep.wav"), wavSpec{rate: 8000}, cat(silence(8000, 0.05), sine(8000, 440, 0.2, -6)))
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hi"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	out, code := lensRun(t, dir)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	mustContain(t, out, "lens: 2 images, 1 sound", "1 other file, 1 folder left out",
		"clear background, 75%", "20x20+10+10", "blank #000000",
		"beep.wav", "50 ms / 0 ms")
	mustLack(t, out, "notes.txt")
}

func TestTableWithBadFile(t *testing.T) {
	dir := t.TempDir()
	good := savePNG(t, dir, "ok.png", opaqueRGB(canvas(8, 8, white)))
	bad := filepath.Join(dir, "bad.png")
	os.WriteFile(bad, []byte("\x89PNG\r\n\x1a\nbroken"), 0o644)
	out, code := lensRun(t, good, bad, filepath.Join(dir, "missing.png"))
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "1 image, 1 file not read", "missing.png: no such file", "not read", "bad.png: can't decode it as PNG")
}

func TestTableBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		savePNG(t, dir, "img"+string(rune('a'+i%26))+string(rune('a'+i/26))+".png", opaqueRGB(canvas(4, 4, white)))
	}
	out, _ := lensRun(t, dir, "-b", "10")
	mustContain(t, out, "… 23 more images")
	if n := strings.Count(out, "\n"); n > 14 {
		t.Errorf("%d lines for a budget of 10:\n%s", n, out)
	}
}

func TestCLI(t *testing.T) {
	out, code := lensRun(t, "-V")
	if code != 0 || out != "lens "+version+"\n" {
		t.Errorf("-V: %q %d", out, code)
	}
	out, code = lensRun(t, "--help")
	if code != 0 || !strings.Contains(out, "lens diff") {
		t.Errorf("--help: %d\n%s", code, out)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "give an image or audio file"},
		{[]string{"-x"}, "unknown flag -x"},
		{[]string{"diff", "a.png"}, "lens diff takes two images"},
		{[]string{"a.png", "-o", "x.jpg"}, "-o writes a PNG picture"},
		{[]string{"a.png", "--fuzz", "300"}, "--fuzz takes a percent"},
		{[]string{"-"}, "lens reads files, not standard input"},
		{[]string{"-b"}, "-b needs a value"},
	} {
		out, code := lensRun(t, c.args...)
		if code != 2 || !strings.Contains(out, c.want) {
			t.Errorf("%v: exit %d, %q", c.args, code, out)
		}
	}
}

func TestNotMedia(t *testing.T) {
	dir := t.TempDir()
	svg := filepath.Join(dir, "logo.svg")
	os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`), 0o644)
	out, code := lensRun(t, svg)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "an SVG is a vector image")
	txt := filepath.Join(dir, "BM.txt")
	os.WriteFile(txt, []byte("BM is not a bitmap, just text that starts with BM"), 0o644)
	out, _ = lensRun(t, txt)
	mustContain(t, out, "not an image or a sound")
	empty := filepath.Join(dir, "e.png")
	os.WriteFile(empty, nil, 0o644)
	out, _ = lensRun(t, empty)
	mustContain(t, out, "an empty file")
	pic := filepath.Join(dir, "x.png")
	savePNG(t, dir, "x.png", opaqueRGB(canvas(4, 4, white)))
	out, code = lensRun(t, pic, "-o", filepath.Join(dir, "y.png"))
	if code != 2 || !strings.Contains(out, "nothing to draw") {
		t.Errorf("-o on an image: %d %s", code, out)
	}
}

func TestSniff(t *testing.T) {
	for _, c := range []struct {
		head, name string
		want       fileKind
	}{
		{"\x89PNG\r\n\x1a\n", "a", kPNG},
		{"\xff\xd8\xff\xe0", "a", kJPEG},
		{"RIFF\x00\x00\x00\x00WAVEfmt ", "a", kWAV},
		{"RIFF\x00\x00\x00\x00WEBPVP8 ", "a", kWebP},
		{"OggS\x00\x02" + strings.Repeat("\x00", 22) + "\x01vorbis", "a", kVorbis},
		{"OggS\x00\x02" + strings.Repeat("\x00", 22) + "OpusHead", "a", kOgg},
		{"ID3\x04", "a", kMP3},
		{"\xff\xfb\x90\x44", "a", kMP3},
		{"fLaC", "a", kFLAC},
		{"\x00\x00\x00\x20ftypM4A ", "a", kM4A},
		{"\x00\x00\x00\x20ftypisom", "a", kMedia},
		{"hello", "a.mp4", kMedia},
		{"hello", "a.txt", kOther},
	} {
		if got := sniff([]byte(c.head), c.name); got != c.want {
			t.Errorf("sniff(%q, %q) = %v, want %v", c.head, c.name, got, c.want)
		}
	}
}

func TestOpusAsMedia(t *testing.T) {
	needFFmpeg(t)
	// a .webm-less Matroska audio-only file goes through ffmpeg as a sound
	out, code := lensRun(t, "testdata/tone.opus")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
}
