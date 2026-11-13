package main

import (
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"
	"sync"
)

// result is one file of a table, analyzed.
type result struct {
	it    item
	img   *imageFacts
	snd   *soundFacts
	err   error
	other bool // not an image or a sound
}

// runMany writes one line per file, images and sounds in their own tables.
func runMany(w io.Writer, items []item, notes []string, o options) int {
	badCode := 0
	res := make([]result, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, min(runtime.NumCPU(), 8))
	for i, it := range items {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := result{it: it}
			switch {
			case it.kind.isImage():
				r.img, r.err = loadImage(it.path, it.kind)
				if r.err == nil {
					analyzeImage(r.img)
					r.img.raw = nil
					r.img.img = nil // the pixels aren't needed for a row
				}
			case it.kind.isAudio():
				r.snd, r.err = analyzeAudioFile(it.path, it.kind, o)
				if r.snd != nil {
					r.snd.spec = nil
				}
			default:
				r.other = true
				r.err = fmt.Errorf("%s", it.kind.notMedia())
			}
			res[i] = r
		}()
	}
	wg.Wait()

	var imgs, snds, bad []result
	for _, r := range res {
		switch {
		case r.err != nil:
			bad = append(bad, r)
		case r.img != nil:
			imgs = append(imgs, r)
		default:
			snds = append(snds, r)
		}
	}
	var what []string
	if len(imgs) > 0 {
		what = append(what, plural(len(imgs), "image"))
	}
	if len(snds) > 0 {
		what = append(what, plural(len(snds), "sound"))
	}
	if len(bad) > 0 {
		what = append(what, fmt.Sprintf("%s not read", plural(len(bad), "file")))
	}
	if len(what) == 0 {
		what = append(what, "nothing to show")
	}
	fmt.Fprintf(w, "lens: %s\n", strings.Join(what, ", "))
	for _, n := range notes {
		fmt.Fprintf(w, "  %s\n", n)
	}
	room := 1 << 30
	if o.budget > 0 {
		room = max(o.budget-1-len(notes), 6)
	}
	// share the rows between the tables, each at least a few
	sections := 0
	for _, n := range []int{len(imgs), len(snds), len(bad)} {
		if n > 0 {
			sections++
		}
	}
	each := func(n int) int {
		if sections == 0 {
			return 0
		}
		return max(3, room/sections-2)
	}
	if len(imgs) > 0 {
		rows := [][]string{{"image", "size", "alpha", "drawn", "colors"}}
		for _, r := range imgs {
			f := r.img
			rows = append(rows, []string{f.path, fmt.Sprintf("%dx%d", f.w, f.h), alphaCell(f), drawnCell(f), colorsCell(f)})
		}
		writeTable(w, rows, each(len(imgs)), "image")
	}
	if len(snds) > 0 {
		rows := [][]string{{"sound", "length", "format", "peak", "LUFS", "silence start/end", "waveform"}}
		for _, r := range snds {
			s := r.snd
			rows = append(rows, soundRow(s))
		}
		writeTable(w, rows, each(len(snds)), "sound")
		warned := false
		for _, r := range snds {
			if r.snd.warning != "" {
				if !warned {
					fmt.Fprintln(w, "warnings")
					warned = true
				}
				fmt.Fprintf(w, "  %s: %s\n", r.snd.path, r.snd.warning)
			}
		}
		if warned {
			badCode = 2
		}
	}
	if len(bad) > 0 {
		fmt.Fprintln(w, "not read")
		n := each(len(bad))
		for i, r := range bad {
			if i == n && len(bad) > n+1 {
				fmt.Fprintf(w, "  … %s more\n", plural(len(bad)-n, "file"))
				break
			}
			fmt.Fprintf(w, "  %s: %v\n", r.it.path, r.err)
		}
		return 2
	}
	return badCode
}

func soundRow(s *soundFacts) []string {
	if s.frames == 0 {
		return []string{s.path, "0 s", s.info.format, "", "", "", ""}
	}
	format := s.info.format + " " + rateText(s.rate) + " " + channelsText(s.ch)
	peak := strings.TrimSuffix(dbText(s.maxPeak()), "FS")
	if s.clipRuns > 0 || s.over > 0 {
		peak += " clipped"
	}
	lufs := "silent"
	if !math.IsInf(s.lufs, -1) {
		lufs = fmt.Sprintf("%.1f", s.lufs)
		if s.lufsEst {
			lufs = "≈" + lufs
		}
	}
	sil := "all silent"
	wave := ""
	if i0, i1 := s.loudSpan(); i0 >= 0 {
		bd := s.blockDur()
		sil = secText(float64(i0)*bd) + " / " + secText(max(0, s.duration()-float64(i1+1)*bd))
		wave = "│" + s.waveRow(24) + "│"
	}
	return []string{s.path, secText(s.duration()), format, peak, lufs, sil, wave}
}

func alphaCell(f *imageFacts) string {
	total := f.w * f.h
	switch {
	case f.bg.kind == bgChecker:
		return "painted checkerboard"
	case f.clear == total:
		return "empty (all clear)"
	case f.clear > 0 && f.cornersClear:
		return "clear background, " + pct(f.clear, total)
	case f.clear > 0:
		return pct(f.clear, total) + " clear, corners not"
	case f.partly > 0:
		return pct(f.partly, total) + " partly clear"
	case f.canAlpha:
		return "opaque (has alpha)"
	}
	return "opaque"
}

func drawnCell(f *imageFacts) string {
	switch {
	case f.drawn.Empty():
		return "nothing"
	case f.drawn.Dx() == f.w && f.drawn.Dy() == f.h:
		return "whole"
	}
	return geom(f.drawn)
}

func colorsCell(f *imageFacts) string {
	if f.visible == 0 {
		return ""
	}
	if f.distinct == 1 {
		return "blank " + hex(f.colors[0].c)
	}
	var cs []string
	for i, c := range f.colors {
		if i == 3 {
			break
		}
		cs = append(cs, hex(c.c))
	}
	return strings.Join(cs, " ")
}

// writeTable aligns rows (the first is the header), at most n of them, with
// a count of the rest.
func writeTable(w io.Writer, rows [][]string, n int, word string) {
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if i == 0 {
				c = clipMiddle(c, 40)
			}
			widths[i] = max(widths[i], runeLen(c))
		}
	}
	for i, r := range rows {
		if i > n && len(rows)-1 > n {
			fmt.Fprintf(w, "  … %s\n", strings.Replace(plural(len(rows)-1-n, word), " ", " more ", 1))
			return
		}
		var cells []string
		for j, c := range r {
			if j == 0 {
				c = clipMiddle(c, 40)
			}
			cells = append(cells, c+strings.Repeat(" ", widths[j]-runeLen(c)))
		}
		fmt.Fprintln(w, strings.TrimRight("  "+strings.Join(cells, "  "), " "))
	}
}

func runeLen(s string) int { return len([]rune(s)) }

// clipMiddle keeps the start and the end of a long name.
func clipMiddle(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	keep := n - 1
	return string(r[:keep/2]) + "…" + string(r[len(r)-(keep-keep/2):])
}
