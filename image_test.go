package main

import (
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

var (
	white  = color.NRGBA{255, 255, 255, 255}
	black  = color.NRGBA{0, 0, 0, 255}
	violet = color.NRGBA{143, 124, 255, 255}
	clear  = color.NRGBA{}
)

func TestTransparentIcon(t *testing.T) {
	m := canvas(200, 100, clear)
	rect(m, image.Rect(50, 20, 150, 80), violet)
	rect(m, image.Rect(49, 20, 50, 80), color.NRGBA{143, 124, 255, 100}) // a soft edge
	p := savePNG(t, t.TempDir(), "icon.png", m)
	out, code := lensRun(t, p)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "PNG, 200x100 landscape 2:1, RGBA 8-bit",
		"the border is clear: a real transparent background",
		"drawn     101x60+49+20",
		"#8f7cff",
		"next: magick "+p+" -crop 101x60+49+20 +repage")
	mustLack(t, out, "\nborder")
}

func TestOpaqueNoAlphaChannel(t *testing.T) {
	m := canvas(64, 64, white)
	rect(m, image.Rect(10, 12, 30, 40), black)
	p := savePNG(t, t.TempDir(), "rgb.png", opaqueRGB(m))
	out, _ := lensRun(t, p)
	mustContain(t, out, "RGB 8-bit", "none: no alpha channel, every pixel is opaque",
		"plain #ffffff (100% of the border)", "drawn     20x28+10+12")
}

func TestAlphaChannelAllOpaque(t *testing.T) {
	// Go's encoder writes an opaque image as RGB, so the file is made by hand
	m := canvas(16, 16, violet)
	p := filepath.Join(t.TempDir(), "o.png")
	os.WriteFile(p, rawRGBAPNG(m), 0o644)
	out, _ := lensRun(t, p)
	mustContain(t, out, "RGBA 8-bit", "an alpha channel, but every pixel is opaque")
}

func TestSixteenBit(t *testing.T) {
	m := image.NewNRGBA64(image.Rect(0, 0, 8, 8))
	m.SetNRGBA64(2, 2, color.NRGBA64{65535, 0, 0, 65535})
	p := savePNG(t, t.TempDir(), "deep.png", m)
	out, _ := lensRun(t, p)
	mustContain(t, out, "RGBA 16-bit", "drawn     1x1+2+2")
}

func TestPaintedCheckerboard(t *testing.T) {
	m := canvas(160, 120, white)
	for y := 0; y < 120; y++ {
		for x := 0; x < 160; x++ {
			if (x/16+y/16)%2 == 1 {
				m.SetNRGBA(x, y, color.NRGBA{204, 204, 204, 255})
			}
		}
	}
	rect(m, image.Rect(60, 40, 100, 90), color.NRGBA{255, 60, 60, 255})
	p := savePNG(t, t.TempDir(), "fake.png", opaqueRGB(m))
	out, _ := lensRun(t, p)
	mustContain(t, out, "a checkerboard painted into the pixels (#ffffff and #cccccc squares of 16 px): fake transparency",
		"drawn     40x50+60+40", "(background)")
}

func TestCheckerNotInAPhoto(t *testing.T) {
	// stripes of two grays that don't swap a square down aren't a checkerboard
	m := canvas(160, 120, white)
	for y := 0; y < 120; y++ {
		for x := 0; x < 160; x++ {
			if (x/16)%2 == 1 {
				m.SetNRGBA(x, y, color.NRGBA{204, 204, 204, 255})
			}
		}
	}
	p := savePNG(t, t.TempDir(), "stripes.png", opaqueRGB(m))
	out, _ := lensRun(t, p)
	mustLack(t, out, "checkerboard")
}

func TestBlankAndEmpty(t *testing.T) {
	dir := t.TempDir()
	out, _ := lensRun(t, savePNG(t, dir, "blank.png", opaqueRGB(canvas(64, 36, black))))
	mustContain(t, out, "64x36 landscape 16:9", "one: every pixel is #000000 (blank)", "drawn     nothing")
	out, _ = lensRun(t, savePNG(t, dir, "empty.png", canvas(10, 10, clear)))
	mustContain(t, out, "every pixel is fully clear: the image is empty", "nothing: every pixel is clear", "none visible")
}

func TestFaintPixels(t *testing.T) {
	m := canvas(100, 100, clear)
	for i := 0; i < 100; i += 7 {
		m.SetNRGBA(i, i, color.NRGBA{0, 0, 0, 5}) // generator noise
	}
	rect(m, image.Rect(30, 40, 60, 70), violet)
	p := savePNG(t, t.TempDir(), "faint.png", m)
	out, _ := lensRun(t, p)
	mustContain(t, out, "drawn     30x30+30+40", "(alpha 16 and up)", "faint pixels (alpha under 16) reach out to 99x99+0+0")
}

func TestJPEGContentBox(t *testing.T) {
	m := canvas(300, 200, white)
	rect(m, image.Rect(100, 50, 181, 121), black)
	p := filepath.Join(t.TempDir(), "plain.jpg")
	f, _ := os.Create(p)
	jpeg.Encode(f, m, &jpeg.Options{Quality: 75})
	f.Close()
	out, _ := lensRun(t, p)
	mustContain(t, out, "JPEG, 300x200 landscape 3:2, YCbCr 8-bit", "plain #ffffff", "drawn     81x71+100+50")
}

func TestAnimatedGIF(t *testing.T) {
	g := &gif.GIF{}
	for i := 0; i < 3; i++ {
		fr := image.NewPaletted(image.Rect(0, 0, 20, 10), palette.Plan9)
		fr.SetColorIndex(i, 0, 3)
		g.Image = append(g.Image, fr)
		g.Delay = append(g.Delay, 40)
	}
	p := filepath.Join(t.TempDir(), "a.gif")
	f, _ := os.Create(p)
	if err := gif.EncodeAll(f, g); err != nil {
		t.Fatal(err)
	}
	f.Close()
	out, _ := lensRun(t, p)
	mustContain(t, out, "GIF, 20x10 landscape 2:1, palette of 256 colors, 3 frames over 1.20 s (the first is shown)")
}

func TestPaletteWithTRNS(t *testing.T) {
	pal := color.Palette{color.NRGBA{0, 0, 0, 0}, color.NRGBA{255, 0, 0, 255}}
	m := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
	m.SetColorIndex(3, 3, 1)
	p := savePNG(t, t.TempDir(), "pal.png", m)
	out, _ := lensRun(t, p)
	mustContain(t, out, "palette of 2 colors with tRNS transparency", "drawn     1x1+3+3", "one: every visible pixel is #ff0000")
}

func TestWebP(t *testing.T) {
	out, _ := lensRun(t, "testdata/lossless.webp")
	mustContain(t, out, "WebP, 64x48 landscape 4:3, lossless", "real transparent background", "drawn     32x24+8+8")
	out, _ = lensRun(t, "testdata/lossy.webp")
	mustContain(t, out, "WebP, 64x48 landscape 4:3, lossy", "no alpha channel")
}

func TestShape(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want string
	}{
		{1920, 1080, "1920x1080 landscape 16:9"},
		{1366, 768, "1366x768 landscape ≈16:9"},
		{1024, 1536, "1024x1536 portrait 2:3"},
		{512, 512, "512x512 square"},
		{1000, 300, "1000x300 landscape"},
		{1080, 1920, "1080x1920 portrait 9:16"},
	} {
		if got := shape(c.w, c.h); got != c.want {
			t.Errorf("shape(%d, %d) = %q, want %q", c.w, c.h, got, c.want)
		}
	}
}

func TestPct(t *testing.T) {
	for _, c := range []struct {
		n, total int
		want     string
	}{{0, 10, "0%"}, {10, 10, "100%"}, {1, 10000, "under 0.1%"}, {382, 1000, "38%"}, {41, 1000, "4.1%"}, {40, 1000, "4%"}, {9999, 10000, "over 99.9%"}} {
		if got := pct(c.n, c.total); got != c.want {
			t.Errorf("pct(%d, %d) = %q, want %q", c.n, c.total, got, c.want)
		}
	}
}

func TestBrokenImage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.png")
	os.WriteFile(p, []byte("\x89PNG\r\n\x1a\nnot really"), 0o644)
	out, code := lensRun(t, p)
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "can't decode it as PNG")
}
