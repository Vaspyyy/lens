package main

import (
	"image"
	"image/color"
	"math/bits"
	"sort"
)

// analyzeImage fills in what the pixels say: transparency, background, the
// drawn area and the main colors.
func analyzeImage(f *imageFacts) {
	m := f.img
	w, h := f.w, f.h
	if w == 0 || h == 0 {
		return
	}
	var hist [1 << 15]int32
	var sums [1 << 15][3]uint64      // uint32 wraps past 16.8M pixels of one bin
	seen := make([]uint64, 1<<24/64) // every 24-bit color seen, a bit each
	any0 := image.Rectangle{Min: image.Point{w, h}}
	strong := any0
	for y := 0; y < h; y++ {
		row := m.Pix[y*m.Stride : y*m.Stride+4*w]
		for x := 0; x < w; x++ {
			r, g, b, a := row[4*x], row[4*x+1], row[4*x+2], row[4*x+3]
			switch {
			case a == 0:
				f.clear++
				continue
			case a < 255:
				f.partly++
			}
			grow(&any0, x, y)
			if a >= 16 {
				grow(&strong, x, y)
			}
			if a < 128 {
				continue
			}
			f.visible++
			k := int(r>>3)<<10 | int(g>>3)<<5 | int(b>>3)
			hist[k]++
			s := &sums[k]
			s[0] += uint64(r)
			s[1] += uint64(g)
			s[2] += uint64(b)
			c := uint32(r)<<16 | uint32(g)<<8 | uint32(b)
			seen[c>>6] |= 1 << (c & 63)
		}
	}
	for _, v := range seen {
		f.distinct += bits.OnesCount64(v)
	}
	corner := func(x, y int) bool { return m.Pix[y*m.Stride+4*x+3] == 0 }
	f.cornersClear = corner(0, 0) && corner(w-1, 0) && corner(0, h-1) && corner(w-1, h-1)
	n, clearN := 0, 0
	edge := func(x, y int) {
		n++
		if corner(x, y) {
			clearN++
		}
	}
	for x := 0; x < w; x++ {
		edge(x, 0)
		if h > 1 {
			edge(x, h-1)
		}
	}
	for y := 1; y < h-1; y++ {
		edge(0, y)
		if w > 1 {
			edge(w-1, y)
		}
	}
	f.borderClear = float64(clearN) / float64(n)
	// rounded corners: on each side, the clear pixels are short runs from
	// both ends, each under a quarter of the side, and none in between
	runs := func(n int, clearAt func(int) bool) bool {
		a := 0
		for a < n && clearAt(a) {
			a++
		}
		b := 0
		for b < n-a && clearAt(n-1-b) {
			b++
		}
		if a == 0 || b == 0 || a*4 > n || b*4 > n {
			return false
		}
		for i := a; i < n-b; i++ {
			if clearAt(i) {
				return false
			}
		}
		return true
	}
	f.rounded = w >= 8 && h >= 8 &&
		runs(w, func(i int) bool { return corner(i, 0) }) && runs(w, func(i int) bool { return corner(i, h-1) }) &&
		runs(h, func(i int) bool { return corner(0, i) }) && runs(h, func(i int) bool { return corner(w-1, i) })

	f.colors = mainColors(&hist, &sums, f.visible)
	full := image.Rect(0, 0, w, h)
	switch {
	case f.clear == w*h:
		f.bg.kind = bgClear
		f.drawn = image.Rectangle{}
		return
	case f.clear > 0:
		f.bg.kind = bgClear
		f.drawn = strong
		if strong.Empty() {
			f.drawn = any0
		} else if any0 != strong {
			f.faint = any0
		}
		if f.drawn != full {
			return
		}
		// clear pixels inside, but the drawing reaches every edge: maybe a
		// plain or checkered border around it
		f.bg = background{}
		f.faint = image.Rectangle{}
	}
	f.bg = findBackground(f)
	switch f.bg.kind {
	case bgPlain:
		f.drawn = contentBox(f, func(r, g, b uint8) bool { return !near(r, g, b, f.bg.c1, tolerance(f)) })
	case bgChecker:
		f.drawn = contentBox(f, func(r, g, b uint8) bool {
			return !near(r, g, b, f.bg.c1, tolerance(f)) && !near(r, g, b, f.bg.c2, tolerance(f))
		})
	default:
		f.drawn = full
	}
	for i := range f.colors {
		c := f.colors[i].c
		if (f.bg.kind == bgPlain || f.bg.kind == bgChecker) && (near(c.R, c.G, c.B, f.bg.c1, 24) || f.bg.kind == bgChecker && near(c.R, c.G, c.B, f.bg.c2, 24)) {
			f.colors[i].label = "background"
		}
	}
}

func grow(r *image.Rectangle, x, y int) {
	if r.Empty() { // nothing yet: a grown box is never empty
		*r = image.Rect(x, y, x+1, y+1)
		return
	}
	r.Min.X = min(r.Min.X, x)
	r.Min.Y = min(r.Min.Y, y)
	r.Max.X = max(r.Max.X, x+1)
	r.Max.Y = max(r.Max.Y, y+1)
}

func area(r image.Rectangle) int {
	if r.Empty() {
		return 0
	}
	return r.Dx() * r.Dy()
}

// tolerance is how far a channel may be from a background color and still
// be background: a little for lossless images, more for JPEG noise.
func tolerance(f *imageFacts) int {
	if f.lossy {
		return 24
	}
	return 6
}

func near(r, g, b uint8, c color.NRGBA, tol int) bool {
	return absDiff(r, c.R) <= tol && absDiff(g, c.G) <= tol && absDiff(b, c.B) <= tol
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// mainColors merges the histogram's bins into colors, largest first, the
// ones with at least 1% of the visible pixels.
func mainColors(hist *[1 << 15]int32, sums *[1 << 15][3]uint64, visible int) []colorShare {
	if visible == 0 {
		return nil
	}
	type bin struct{ k, n int }
	var bins []bin
	for k, n := range hist {
		if n > 0 {
			bins = append(bins, bin{k, int(n)})
		}
	}
	sort.Slice(bins, func(i, j int) bool { return bins[i].n > bins[j].n || bins[i].n == bins[j].n && bins[i].k < bins[j].k })
	if len(bins) > 512 {
		bins = bins[:512]
	}
	type group struct {
		n          int
		sr, sg, sb uint64
		cr, cg, cb int // the first bin's color, the group's center
	}
	var groups []*group
	for _, b := range bins {
		s := sums[b.k]
		r, g, bl := int(s[0])/b.n, int(s[1])/b.n, int(s[2])/b.n
		var to *group
		for _, gr := range groups {
			dr, dg, db := r-gr.cr, g-gr.cg, bl-gr.cb
			if dr*dr+dg*dg+db*db <= 28*28 {
				to = gr
				break
			}
		}
		if to == nil {
			to = &group{cr: r, cg: g, cb: bl}
			groups = append(groups, to)
		}
		to.n += b.n
		to.sr += s[0]
		to.sg += s[1]
		to.sb += s[2]
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].n > groups[j].n })
	var out []colorShare
	for _, g := range groups {
		if len(out) == 6 || g.n*100 < visible {
			break
		}
		n := uint64(g.n)
		out = append(out, colorShare{c: color.NRGBA{uint8(g.sr / n), uint8(g.sg / n), uint8(g.sb / n), 255}, n: g.n})
	}
	return out
}

// findBackground looks at the border: one color on nearly all of it, or a
// checkerboard painted in (fake transparency).
func findBackground(f *imageFacts) background {
	m := f.img
	w, h := f.w, f.h
	tol := tolerance(f)
	var border []color.NRGBA
	at := func(x, y int) color.NRGBA {
		i := y*m.Stride + 4*x
		return color.NRGBA{m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3]}
	}
	for x := 0; x < w; x++ {
		border = append(border, at(x, 0))
		if h > 1 {
			border = append(border, at(x, h-1))
		}
	}
	for y := 1; y < h-1; y++ {
		border = append(border, at(0, y))
		if w > 1 {
			border = append(border, at(w-1, y))
		}
	}
	// the most common color of the border, by 5-bit bins
	counts := map[int]int{}
	for _, c := range border {
		counts[int(c.R>>3)<<10|int(c.G>>3)<<5|int(c.B>>3)]++
	}
	best, bestN := 0, -1
	for k, n := range counts {
		if n > bestN || n == bestN && k < best {
			best, bestN = k, n
		}
	}
	var sr, sg, sb, n int
	for _, c := range border {
		if int(c.R>>3)<<10|int(c.G>>3)<<5|int(c.B>>3) == best {
			sr += int(c.R)
			sg += int(c.G)
			sb += int(c.B)
			n++
		}
	}
	mode := color.NRGBA{uint8(sr / n), uint8(sg / n), uint8(sb / n), 255}
	match := 0
	for _, c := range border {
		if c.A >= 128 && near(c.R, c.G, c.B, mode, tol) {
			match++
		}
	}
	share := float64(match) / float64(len(border))
	if share >= 0.9 {
		return background{kind: bgPlain, c1: mode, share: share}
	}
	if c1, c2, size, ok := findChecker(f); ok {
		match := 0
		for _, c := range border {
			if near(c.R, c.G, c.B, c1, tol) || near(c.R, c.G, c.B, c2, tol) {
				match++
			}
		}
		share := float64(match) / float64(len(border))
		if share >= 0.7 {
			return background{kind: bgChecker, c1: c1, c2: c2, share: share, squareSize: size}
		}
	}
	return background{}
}

// findChecker looks for two grays alternating in squares along a row near
// the top, and the opposite order one square further down.
func findChecker(f *imageFacts) (color.NRGBA, color.NRGBA, int, bool) {
	m := f.img
	w, h := f.w, f.h
	tol := max(tolerance(f), 10)
	if w < 16 || h < 16 {
		return color.NRGBA{}, color.NRGBA{}, 0, false
	}
	at := func(x, y int) color.NRGBA {
		i := y*m.Stride + 4*x
		return color.NRGBA{m.Pix[i], m.Pix[i+1], m.Pix[i+2], 255}
	}
	gray := func(c color.NRGBA) bool {
		hi := max(c.R, c.G, c.B)
		lo := min(c.R, c.G, c.B)
		return int(hi)-int(lo) <= 24
	}
	lum := func(c color.NRGBA) int { return (int(c.R)*299 + int(c.G)*587 + int(c.B)*114) / 1000 }
	for _, y := range []int{1, 2, h / 3} {
		if y >= h {
			continue
		}
		// runs of one color along the row
		type run struct {
			c      color.NRGBA
			x0, n  int
			colorA bool
		}
		var runs []run
		for x := 0; x < w; {
			c := at(x, y)
			x0 := x
			for x < w && near(at(x, y).R, at(x, y).G, at(x, y).B, c, tol) {
				x++
			}
			runs = append(runs, run{c: c, x0: x0, n: x - x0})
		}
		if len(runs) < 4 {
			continue
		}
		a, b := runs[0].c, runs[1].c
		if !gray(a) || !gray(b) {
			continue
		}
		d := lum(a) - lum(b)
		if d < 0 {
			d = -d
		}
		if d < 8 || d > 140 {
			continue
		}
		// the inner runs alternate between a and b and have one length
		size := runs[1].n
		ok := size >= 4 && size <= 128
		covered := 0
		for i, r := range runs {
			want := a
			if i%2 == 1 {
				want = b
			}
			if !near(r.c.R, r.c.G, r.c.B, want, tol) {
				ok = false
				break
			}
			if i > 0 && i < len(runs)-1 && (r.n < size-1 || r.n > size+1) {
				ok = false
				break
			}
			covered += r.n
		}
		if !ok || covered < w*9/10 {
			continue
		}
		// one square down, the colors swap
		y2 := y + size
		if y2 >= h {
			y2 = y - size
		}
		if y2 < 0 {
			continue
		}
		agree, total := 0, 0
		for i := 1; i < len(runs)-1; i++ {
			mid := runs[i].x0 + runs[i].n/2
			other := b
			if i%2 == 1 {
				other = a
			}
			c := at(mid, y2)
			total++
			if near(c.R, c.G, c.B, other, tol) {
				agree++
			}
		}
		if total >= 2 && agree*5 >= total*4 {
			if lum(a) < lum(b) {
				a, b = b, a
			}
			return a, b, size, true
		}
	}
	return color.NRGBA{}, color.NRGBA{}, 0, false
}

// contentBox is the box around the pixels that are content. A row or column
// needs a few such pixels in a lossy image, so JPEG noise doesn't count.
func contentBox(f *imageFacts, content func(r, g, b uint8) bool) image.Rectangle {
	m := f.img
	w, h := f.w, f.h
	rows := make([]int, h)
	cols := make([]int, w)
	for y := 0; y < h; y++ {
		row := m.Pix[y*m.Stride : y*m.Stride+4*w]
		for x := 0; x < w; x++ {
			if row[4*x+3] >= 128 && content(row[4*x], row[4*x+1], row[4*x+2]) {
				rows[y]++
				cols[x]++
			}
		}
	}
	need := 1
	if f.lossy {
		need = max(2, min(w, h)/200)
	}
	first := func(c []int) int {
		for i, n := range c {
			if n >= need {
				return i
			}
		}
		return -1
	}
	last := func(c []int) int {
		for i := len(c) - 1; i >= 0; i-- {
			if c[i] >= need {
				return i
			}
		}
		return -1
	}
	y0, x0 := first(rows), first(cols)
	if y0 < 0 || x0 < 0 {
		return image.Rectangle{}
	}
	return image.Rect(x0, y0, last(cols)+1, last(rows)+1)
}
