package main

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

const (
	cell    = 8 // changed pixels are grouped on a grid of this size
	gap     = 4 // spots closer than this many cells are one
	maxShow = 6
)

type spot struct {
	box image.Rectangle
	n   int
}

type diffResult struct {
	a, b      *imageFacts
	w, h      int // the compared area
	fuzz      float64
	changed   int
	strong    int // changed by more than 10%
	maxDiff   int // 0-255
	spots     []spot
	all       image.Rectangle
	mask      []bool
	sameBytes bool
}

func runDiff(out io.Writer, o options) int {
	var fs [2]*imageFacts
	for i, p := range o.files {
		k, err := sniffFile(p)
		if err != nil {
			fmt.Fprintf(out, "lens: %v\n", err)
			return 2
		}
		if !k.isImage() {
			if k.isAudio() {
				fmt.Fprintf(out, "lens: %s is a sound; lens diff compares images (lens A B lists two sounds side by side)\n", p)
			} else {
				fmt.Fprintf(out, "lens: %s: %s\n", p, k.notMedia())
			}
			return 2
		}
		f, err := loadImage(p, k)
		if err != nil {
			fmt.Fprintf(out, "lens: %s: %v\n", p, err)
			return 2
		}
		fs[i] = f
	}
	o.pad = min(o.pad, max(fs[0].w, fs[0].h, fs[1].w, fs[1].h)) // no overflow in the boxes
	d := compare(fs[0], fs[1], o)
	renderDiff(out, d, o)
	if o.picture != "" && d.changed == 0 {
		fmt.Fprintf(out, "picture  not written: nothing changed\n")
	}
	if o.picture != "" && d.changed > 0 {
		if err := writeDiffPicture(o.picture, d, cropFor(d, o)); err != nil {
			fmt.Fprintf(out, "lens: %v\n", err)
			return 2
		}
		fmt.Fprintf(out, "picture  %s: before, after, and after with the changes in red, side by side\n", o.picture)
	}
	return 0
}

// compare finds the changed pixels and groups them into spots.
func compare(a, b *imageFacts, o options) *diffResult {
	d := &diffResult{a: a, b: b, w: min(a.w, b.w), h: min(a.h, b.h), fuzz: o.fuzz}
	d.sameBytes = bytes.Equal(a.raw, b.raw)
	if d.fuzz < 0 {
		d.fuzz = 0
		if a.lossy || b.lossy {
			d.fuzz = 3
		}
	}
	limit := int(d.fuzz / 100 * 255)
	d.mask = make([]bool, d.w*d.h)
	gw, gh := (d.w+cell-1)/cell, (d.h+cell-1)/cell
	grid := make([]bool, gw*gh)
	pa, pb := a.img, b.img
	for y := 0; y < d.h; y++ {
		ra := pa.Pix[y*pa.Stride:]
		rb := pb.Pix[y*pb.Stride:]
		for x := 0; x < d.w; x++ {
			i := 4 * x
			diff := pixelDiff(ra[i:i+4], rb[i:i+4])
			if diff <= limit {
				continue
			}
			d.mask[y*d.w+x] = true
			d.changed++
			d.maxDiff = max(d.maxDiff, diff)
			if diff*10 > 255 {
				d.strong++
			}
			grid[(y/cell)*gw+x/cell] = true
			grow(&d.all, x, y)
		}
	}
	if d.changed == 0 {
		return d
	}
	// cells within gap of each other are one spot (union-find)
	parent := make([]int, gw*gh)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for gy := 0; gy < gh; gy++ {
		for gx := 0; gx < gw; gx++ {
			if !grid[gy*gw+gx] {
				continue
			}
			for dy := -gap; dy <= 0; dy++ {
				for dx := -gap; dx <= gap; dx++ {
					if dy == 0 && dx >= 0 {
						break
					}
					x, y := gx+dx, gy+dy
					if x < 0 || y < 0 || x >= gw || !grid[y*gw+x] {
						continue
					}
					ra, rb := find(gy*gw+gx), find(y*gw+x)
					if ra != rb {
						parent[ra] = rb
					}
				}
			}
		}
	}
	byRoot := map[int]*spot{}
	for y := 0; y < d.h; y++ {
		for x := 0; x < d.w; x++ {
			if !d.mask[y*d.w+x] {
				continue
			}
			r := find((y/cell)*gw + x/cell)
			s := byRoot[r]
			if s == nil {
				s = &spot{box: image.Rectangle{Min: image.Point{d.w, d.h}}}
				byRoot[r] = s
			}
			grow(&s.box, x, y)
			s.n++
		}
	}
	for _, s := range byRoot {
		d.spots = append(d.spots, *s)
	}
	sort.Slice(d.spots, func(i, j int) bool {
		si, sj := d.spots[i], d.spots[j]
		if si.n != sj.n {
			return si.n > sj.n
		}
		return si.box.Min.Y < sj.box.Min.Y || si.box.Min.Y == sj.box.Min.Y && si.box.Min.X < sj.box.Min.X
	})
	return d
}

// pixelDiff is the largest difference of a channel, on premultiplied
// colors, so pixels that are clear in both are equal whatever their color.
func pixelDiff(a, b []uint8) int {
	aa, ab := int(a[3]), int(b[3])
	m := abs(aa - ab)
	for c := 0; c < 3; c++ {
		m = max(m, abs(int(a[c])*aa/255-int(b[c])*ab/255))
	}
	return m
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// crop is the suggestion: one box for both images, or one per spot.
type crop struct {
	boxes   []image.Rectangle
	perSpot bool
	spots   [][]int         // the spot numbers in each crop
	union   image.Rectangle // the one box around everything, padded
}

func padBox(r image.Rectangle, pad, w, h int) image.Rectangle {
	return image.Rect(max(0, r.Min.X-pad), max(0, r.Min.Y-pad), min(w, r.Max.X+pad), min(h, r.Max.Y+pad))
}

// cropFor picks one crop around all changes, or one per spot when one box
// would mostly show what didn't change. Crops keep the spots' order, so
// crop 1 holds spot 1; every spot is in a crop, listed or not.
func cropFor(d *diffResult, o options) crop {
	c := crop{union: padBox(d.all, o.pad, d.w, d.h)}
	type group struct {
		box   image.Rectangle
		spots []int
	}
	var gs []group
	for i, s := range d.spots {
		gs = append(gs, group{padBox(s.box, o.pad, d.w, d.h), []int{i + 1}})
	}
	// padded boxes that overlap become one, under the first one's number
	for merged := true; merged; {
		merged = false
		for i := 0; i < len(gs) && !merged; i++ {
			for j := i + 1; j < len(gs); j++ {
				if gs[i].box.Overlaps(gs[j].box) {
					gs[i].box = gs[i].box.Union(gs[j].box)
					gs[i].spots = append(gs[i].spots, gs[j].spots...)
					gs = append(gs[:j], gs[j+1:]...)
					merged = true
					break
				}
			}
		}
	}
	sum := 0
	for _, g := range gs {
		sum += area(g.box)
	}
	if len(gs) > 1 && len(gs) <= maxShow && area(c.union) > 2*sum && area(c.union)*4 > d.w*d.h {
		for _, g := range gs {
			sort.Ints(g.spots)
			c.boxes = append(c.boxes, g.box)
			c.spots = append(c.spots, g.spots)
		}
		c.perSpot = true
		return c
	}
	c.boxes = []image.Rectangle{c.union}
	return c
}

func renderDiff(out io.Writer, d *diffResult, o options) {
	cw := &countWriter{w: out}
	w := io.Writer(cw)
	a, b := d.a, d.b
	size := fmt.Sprintf("%dx%d both", a.w, a.h)
	if a.w != b.w || a.h != b.h {
		size = fmt.Sprintf("%dx%d and %dx%d: the sizes differ, so the %dx%d at the top left is compared", a.w, a.h, b.w, b.h, d.w, d.h)
	}
	fmt.Fprintf(w, "lens: diff %s %s  %s\n", a.path, b.path, size)
	if d.changed == 0 {
		same := "every pixel is the same"
		switch {
		case d.sameBytes:
			same = "the files are identical, byte for byte"
		case d.fuzz > 0:
			same = fmt.Sprintf("no pixel differs by more than %s%%", fuzzText(d.fuzz))
		}
		if a.w != b.w || a.h != b.h {
			same = fmt.Sprintf("in the shared %dx%d area, %s; the sizes differ", d.w, d.h, same)
		}
		fmt.Fprintln(w, "same     "+same)
		return
	}
	total := d.w * d.h
	spotWord := "1 spot"
	if len(d.spots) > 1 {
		spotWord = plural(len(d.spots), "spot")
	}
	fuzz := ""
	if d.fuzz > 0 {
		fuzz = fmt.Sprintf(" by more than %s%%", fuzzText(d.fuzz))
	}
	fmt.Fprintf(w, "changed  %s (%s)%s in %s, the largest difference %s\n",
		plural(d.changed, "pixel"), pct(d.changed, total), fuzz, spotWord, pct(d.maxDiff, 255))
	room := 1 << 30
	if o.budget > 0 {
		room = max(o.budget-6, 2)
	}
	shown := min(len(d.spots), maxShow, room)
	gw := 0
	for _, s := range d.spots[:shown] {
		gw = max(gw, len(geom(s.box)))
	}
	for i, s := range d.spots[:shown] {
		fmt.Fprintf(w, "  spot %d  %-*s  %s\n", i+1, gw, geom(s.box), plural(s.n, "pixel"))
	}
	if rest := len(d.spots) - shown; rest > 0 {
		small := 0
		for _, s := range d.spots[shown:] {
			small += s.n
		}
		fmt.Fprintf(w, "  … %s with %s\n", strings.Replace(plural(rest, "spot"), " ", " more ", 1), plural(small, "pixel"))
	}
	if d.strong*5 < d.changed && d.changed > 100 {
		fmt.Fprintf(w, "faint    %s of the changes are under 10%%; --fuzz 10 leaves them out\n", pct(d.changed-d.strong, d.changed))
	}
	c := cropFor(d, o)
	pads := fmt.Sprintf("the changes plus %d px", o.pad)
	if c.perSpot {
		fmt.Fprintf(w, "crop     one per spot, since one crop around all would be %s of the image\n", pct(area(c.union), total))
	} else {
		fmt.Fprintf(w, "crop     %s for both (%s, %s of the image)\n", geom(c.union), pads, pct(area(c.union), total))
	}
	stems := cropStems(a.path, b.path, len(c.boxes), c.perSpot)
	for i, box := range c.boxes {
		suffix := cropSuffix(i, c.perSpot)
		if c.perSpot {
			per := 3
			if o.budget > 0 && cw.n+per > o.budget && i > 0 {
				fmt.Fprintf(w, "  … %s (-b 0 shows them)\n", strings.Replace(plural(len(c.boxes)-i, "crop"), " ", " more ", 1))
				break
			}
			fmt.Fprintf(w, "  %s:\n", spotList(c.spots[i]))
		}
		for j, f := range []*imageFacts{a, b} {
			fmt.Fprintf(w, "  magick %s -crop %s +repage %s\n", cmdPath(f.path), geom(box), cmdPath(sibling(f.path, stems[j]+suffix, ".png")))
		}
	}
}

func cropSuffix(i int, perSpot bool) string {
	if perSpot {
		return fmt.Sprintf("-crop%d", i+1)
	}
	return "-crop"
}

// cropStems picks what goes before "-crop" in the crops' names, so no two
// crops share a file and none overwrites an input: before-crop.png, else
// shot-before-crop.png (shot.png and shot.jpg), else with -lens in it.
func cropStems(a, b string, n int, perSpot bool) [2]string {
	key := func(p string) string {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return filepath.Clean(p)
	}
	for _, st := range [][2]string{{"", ""}, {"-before", "-after"}, {"-lens-before", "-lens-after"}} {
		seen := map[string]bool{key(a): true, key(b): true}
		ok := true
		for i := 0; i < n && ok; i++ {
			for j, p := range []string{a, b} {
				k := key(sibling(p, st[j]+cropSuffix(i, perSpot), ".png"))
				if seen[k] {
					ok = false
					break
				}
				seen[k] = true
			}
		}
		if ok {
			return st
		}
	}
	return [2]string{"-lens-before", "-lens-after"}
}

// countWriter counts the lines written through it.
type countWriter struct {
	w io.Writer
	n int
}

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += bytes.Count(p, []byte("\n"))
	return c.w.Write(p)
}

// spotList is "spot 2" or "spots 2 and 5" or "spots 1, 3 and 4".
func spotList(ns []int) string {
	if len(ns) == 1 {
		return fmt.Sprintf("spot %d", ns[0])
	}
	var s []string
	for _, n := range ns[:len(ns)-1] {
		s = append(s, fmt.Sprint(n))
	}
	return fmt.Sprintf("spots %s and %d", strings.Join(s, ", "), ns[len(ns)-1])
}

func fuzzText(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}
