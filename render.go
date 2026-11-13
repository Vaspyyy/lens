package main

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// geom is ImageMagick's geometry of a box: WxH+X+Y.
func geom(r image.Rectangle) string {
	return fmt.Sprintf("%dx%d+%d+%d", r.Dx(), r.Dy(), r.Min.X, r.Min.Y)
}

func hex(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// pct is a share for people: 38.2%, 0.4%, under 0.1%, 100%.
func pct(n, total int) string {
	if total == 0 || n == 0 {
		return "0%"
	}
	p := float64(n) * 100 / float64(total)
	switch {
	case n == total:
		return "100%"
	case p < 0.1:
		return "under 0.1%"
	case p >= 99.9:
		return "over 99.9%"
	case p >= 10:
		return fmt.Sprintf("%.0f%%", p)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", p), ".0") + "%"
}

// shape is "1920x1080 landscape 16:9".
func shape(w, h int) string {
	s := fmt.Sprintf("%dx%d", w, h)
	switch {
	case w == h:
		return s + " square"
	case w > h:
		s += " landscape"
	default:
		s += " portrait"
	}
	if r := ratio(w, h); r != "" {
		s += " " + r
	}
	return s
}

var ratios = [][2]int{{4, 3}, {3, 2}, {16, 9}, {16, 10}, {21, 9}, {2, 1}, {5, 4}, {3, 1}, {32, 9}}

// ratio names a common aspect ratio, exactly or with ≈ when within 1%.
func ratio(w, h int) string {
	long, short := max(w, h), min(w, h)
	if short == 0 {
		return ""
	}
	for _, r := range ratios {
		want := float64(r[0]) / float64(r[1])
		got := float64(long) / float64(short)
		if d := got/want - 1; d < -0.01 || d > 0.01 {
			continue
		}
		a, b := r[0], r[1]
		if w < h {
			a, b = b, a
		}
		if long*r[1] == short*r[0] {
			return fmt.Sprintf("%d:%d", a, b)
		}
		return fmt.Sprintf("≈%d:%d", a, b)
	}
	return ""
}

func durText(d time.Duration) string {
	s := d.Seconds()
	switch {
	case s < 10:
		return fmt.Sprintf("%.2f s", s)
	case s < 60:
		return fmt.Sprintf("%.1f s", s)
	case s < 3600:
		return fmt.Sprintf("%d:%04.1f", int(s)/60, s-float64(int(s)/60*60))
	}
	return fmt.Sprintf("%d:%02d:%02d", int(s)/3600, int(s)/60%60, int(s)%60)
}

// imageHeader is the first line's facts: format, shape, color type, size.
func imageHeader(f *imageFacts) string {
	parts := []string{string(f.kind), shape(f.w, f.h), f.stored}
	if f.extra != "" {
		parts = append(parts, f.extra)
	}
	if f.frames > 1 {
		parts = append(parts, fmt.Sprintf("%d frames over %s (the first is shown)", f.frames, durText(f.dur)))
	}
	parts = append(parts, sizeText(f.size))
	return strings.Join(parts, ", ")
}

// alphaText says what the transparency is.
func alphaText(f *imageFacts) string {
	total := f.w * f.h
	switch {
	case f.clear == 0 && f.partly == 0 && f.canAlpha:
		return "an alpha channel, but every pixel is opaque"
	case f.clear == 0 && f.partly == 0:
		return "none: no alpha channel, every pixel is opaque"
	case f.clear == total:
		return "every pixel is fully clear: the image is empty"
	}
	s := fmt.Sprintf("%s clear, none partly", pct(f.clear, total))
	if f.partly > 0 {
		s = fmt.Sprintf("%s clear, %s partly (soft edges, glow or shadow)", pct(f.clear, total), pct(f.partly, total))
	}
	switch {
	case f.cornersClear:
		s += "; corners clear: a real transparent background"
	case f.clear > 0:
		s += "; the corners aren't clear"
	}
	return s
}

func bgText(f *imageFacts) string {
	switch f.bg.kind {
	case bgPlain:
		return fmt.Sprintf("plain %s (%s of the border)", hex(f.bg.c1), pct(int(f.bg.share*1000), 1000))
	case bgChecker:
		return fmt.Sprintf("a checkerboard painted into the pixels (%s and %s squares of %d px): fake transparency", hex(f.bg.c1), hex(f.bg.c2), f.bg.squareSize)
	}
	return ""
}

func drawnText(f *imageFacts) string {
	total := f.w * f.h
	switch {
	case f.drawn.Empty() && f.bg.kind == bgClear:
		return "nothing: every pixel is clear"
	case f.drawn.Empty():
		return "nothing: every pixel is background"
	case f.drawn == image.Rect(0, 0, f.w, f.h):
		if f.bg.kind == bgNone {
			return "the whole image (no plain border to trim)"
		}
		return "the whole image"
	}
	s := fmt.Sprintf("%s, %s of the image", geom(f.drawn), pct(area(f.drawn), total))
	switch f.bg.kind {
	case bgClear:
		if !f.faint.Empty() {
			s += " (alpha 16 and up)"
		} else {
			s += " (the rest is clear)"
		}
	case bgPlain:
		s += " (unlike the border color)"
	case bgChecker:
		s += " (unlike the checkerboard)"
	}
	return s
}

func colorsText(f *imageFacts) string {
	if f.visible == 0 {
		return "none visible (every pixel is under half opaque)"
	}
	if f.distinct == 1 {
		return fmt.Sprintf("one: every visible pixel is %s (blank)", hex(f.colors[0].c))
	}
	var parts []string
	for _, c := range f.colors {
		p := hex(c.c) + " " + pct(c.n, f.visible)
		if c.label != "" {
			p += " (" + c.label + ")"
		}
		parts = append(parts, p)
	}
	s := strings.Join(parts, ", ")
	if s == "" {
		s = "no color covers 1%"
	}
	return s + "; " + plural(f.distinct, "distinct color")
}

// renderImage writes the card of one image.
func renderImage(w io.Writer, f *imageFacts, o options) {
	fmt.Fprintf(w, "lens: %s  %s\n", f.path, imageHeader(f))
	line := func(label, text string) {
		if text != "" {
			fmt.Fprintf(w, "%-9s %s\n", label, text)
		}
	}
	line("alpha", alphaText(f))
	line("border", bgText(f))
	line("drawn", drawnText(f))
	if !f.faint.Empty() {
		line("", fmt.Sprintf("faint pixels (alpha under 16) reach out to %s", geom(f.faint)))
	}
	line("colors", colorsText(f))
	if !f.drawn.Empty() && area(f.drawn)*10 < f.w*f.h*9 {
		fmt.Fprintf(w, "next: magick %s -crop %s +repage %s   cut it to the drawn area\n",
			shellQuote(f.path), geom(f.drawn), shellQuote(sibling(f.path, "-trim", ".png")))
	}
}

// sibling is a file next to path with a suffix on its name: a.png -> a-trim.png.
func sibling(path, suffix, ext string) string {
	dir, base := filepath.Split(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return dir + stem + suffix + ext
}
