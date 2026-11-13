package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

// writePNG saves a picture, through a temporary file so a failed write
// leaves nothing half written.
func writePNG(path string, m image.Image) error {
	tmp := path + ".lens-tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("can't write the picture: %v", err)
	}
	if err := png.Encode(f, m); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("can't write the picture: %v", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("can't write the picture: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("can't write the picture: %v", err)
	}
	return nil
}

var (
	picBack  = color.NRGBA{24, 24, 32, 255}
	picWave  = color.NRGBA{137, 180, 250, 255}
	picQuiet = color.NRGBA{60, 60, 80, 255}
)

// writeAudioPicture draws the waveform (linear peaks) over a spectrogram
// (log frequency, level in color).
func writeAudioPicture(path string, s *soundFacts) error {
	const W, waveH, gapH, specH = 1200, 160, 8, 320
	m := image.NewNRGBA(image.Rect(0, 0, W, waveH+gapH+specH))
	fill(m, m.Rect, picBack)
	if s.frames == 0 {
		return writePNG(path, m)
	}
	cols := s.columns(W)
	quiet := make([]bool, W)
	t := float32(dbToLin(s.threshold))
	mid := waveH / 2
	for x := 0; x < W; x++ {
		c := cols[x*len(cols)/W]
		var p float32
		for _, v := range s.blocks[c[0]:c[1]] {
			p = max(p, v)
		}
		// heights in dB from the silence threshold up, as in the text
		lvl := 0.0
		if p > t {
			lvl = (linToDB(float64(min(p, 1))) - s.threshold) / -s.threshold
		}
		half := int(math.Round(lvl * float64(mid-2)))
		col := picWave
		if p <= t {
			col = picQuiet
		}
		quiet[x] = p <= t
		for y := mid - half; y <= mid+half; y++ {
			m.SetNRGBA(x, y, col)
		}
	}
	// spectrogram
	if len(s.spec) > 0 {
		var top float32
		for _, f := range s.spec {
			for _, v := range f.bins {
				top = max(top, v)
			}
		}
		y0 := waveH + gapH
		dur := float64(s.frames)
		for x := 0; x < W; x++ {
			at := int64((float64(x) + 0.5) / W * dur)
			// the frame whose middle is nearest
			lo, hi := 0, len(s.spec)-1
			for lo < hi {
				mid := (lo + hi) / 2
				if s.spec[mid].center < at {
					lo = mid + 1
				} else {
					hi = mid
				}
			}
			if lo > 0 && abs64(s.spec[lo-1].center-at) < abs64(s.spec[lo].center-at) {
				lo--
			}
			f := s.spec[lo]
			for y := 0; y < specH; y++ {
				bin := (specH - 1 - y) * picBins / specH
				v := f.bins[bin]
				db := -120.0
				if v > 0 && top > 0 {
					db = 10 * math.Log10(float64(v/top))
				}
				if quiet[x] { // silent here: the window only smears its neighbors in
					db = -120
				}
				m.SetNRGBA(x, y0+y, heat((db+90)/90))
			}
		}
	}
	return writePNG(path, m)
}

// heat maps 0..1 to dark blue, purple, orange, pale yellow.
func heat(v float64) color.NRGBA {
	stops := []color.NRGBA{{12, 10, 30, 255}, {80, 20, 120, 255}, {190, 55, 90, 255}, {250, 140, 40, 255}, {252, 245, 180, 255}}
	v = max(0, min(1, v))
	p := v * float64(len(stops)-1)
	i := min(int(p), len(stops)-2)
	f := p - float64(i)
	a, b := stops[i], stops[i+1]
	mix := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*f) }
	return color.NRGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 255}
}

func fill(m *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			m.SetNRGBA(x, y, c)
		}
	}
}

// writeDiffPicture puts the crop of before, of after, and of after with
// the changed pixels in red side by side, scaled down when wide.
func writeDiffPicture(path string, d *diffResult, c crop) error {
	box := c.union
	if c.perSpot && len(c.boxes) > 0 {
		box = c.boxes[0]
		for _, b := range c.boxes[1:] {
			box = box.Union(b)
		}
	}
	const sep = 8
	scale := 1
	for (box.Dx()*3+2*sep)/scale > 2400 || box.Dy()/scale > 1600 {
		scale++
	}
	pw, ph := (box.Dx()+scale-1)/scale, (box.Dy()+scale-1)/scale
	m := image.NewNRGBA(image.Rect(0, 0, pw*3+2*sep, ph))
	fill(m, m.Rect, picBack)
	red := color.NRGBA{255, 40, 40, 255}
	for py := 0; py < ph; py++ {
		for px := 0; px < pw; px++ {
			x, y := box.Min.X+px*scale, box.Min.Y+py*scale
			ca := over(d.a.img.NRGBAAt(x, y))
			cb := over(d.b.img.NRGBAAt(x, y))
			m.SetNRGBA(px, py, ca)
			m.SetNRGBA(pw+sep+px, py, cb)
			changed := false
			for dy := 0; dy < scale && !changed; dy++ {
				for dx := 0; dx < scale; dx++ {
					xx, yy := x+dx, y+dy
					if xx < d.w && yy < d.h && d.mask[yy*d.w+xx] {
						changed = true
						break
					}
				}
			}
			dim := color.NRGBA{cb.R / 3, cb.G / 3, cb.B / 3, 255}
			if changed {
				dim = red
			}
			m.SetNRGBA(2*(pw+sep)+px, py, dim)
		}
	}
	return writePNG(path, m)
}

// over puts a pixel on a gray checker-free backdrop so clear parts show.
func over(c color.NRGBA) color.NRGBA {
	if c.A == 255 {
		return c
	}
	a := float64(c.A) / 255
	bg := 128.0
	mix := func(v uint8) uint8 { return uint8(float64(v)*a + bg*(1-a)) }
	return color.NRGBA{mix(c.R), mix(c.G), mix(c.B), 255}
}
