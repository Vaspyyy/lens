package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"time"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// imageFacts is what lens knows about one image.
type imageFacts struct {
	path   string
	kind   fileKind
	size   int64
	raw    []byte
	w, h   int
	stored string // the color type as stored: "RGBA 8-bit", "palette of 37 colors"
	extra  string // "progressive", "interlaced", "lossless"
	// canAlpha: the file can carry transparency (an alpha channel, a
	// transparent palette entry, tRNS)
	canAlpha bool
	frames   int           // animation frames (GIF, APNG, WebP), 0 for a still image
	dur      time.Duration // their total duration
	img      *image.NRGBA  // the (first) frame
	lossy    bool

	// from analyzeImage
	clear, partly, visible int // pixel counts: alpha 0, alpha 1-254, alpha 128+
	cornersClear           bool
	borderClear            float64 // the share of the border's pixels that are clear
	bg                     background
	drawn                  image.Rectangle // what isn't background; empty: nothing
	faint                  image.Rectangle // with faint pixels, when much bigger than drawn
	colors                 []colorShare
	distinct               int
}

// background is what the border of an image is: nothing plain, one color,
// or a painted checkerboard.
type background struct {
	kind       int // bgNone, bgClear, bgPlain, bgChecker
	c1, c2     color.NRGBA
	share      float64 // of the border pixels
	squareSize int
}

const (
	bgNone = iota
	bgClear
	bgPlain
	bgChecker
)

type colorShare struct {
	c     color.NRGBA
	n     int
	label string // "background"
}

// loadImage reads and decodes an image, with the facts its header gives.
func loadImage(path string, kind fileKind) (*imageFacts, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &imageFacts{path: path, kind: kind, size: int64(len(raw)), raw: raw}
	switch kind {
	case kPNG:
		pngFacts(f)
	case kJPEG:
		jpegFacts(f)
		f.lossy = true
	case kWebP:
		if err := webpFacts(f); err != nil {
			return nil, err
		}
	case kBMP:
		if len(raw) >= 30 {
			bpp := binary.LittleEndian.Uint16(raw[28:30])
			f.stored = fmt.Sprintf("%d bits a pixel", bpp)
			// alpha only with a header that has an alpha mask, and one set
			hs := binary.LittleEndian.Uint32(raw[14:18])
			f.canAlpha = bpp == 32 && hs >= 56 && len(raw) >= 70 && binary.LittleEndian.Uint32(raw[66:70]) != 0
		}
	case kTIFF:
		tiffFacts(f)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil && int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, fmt.Errorf("%dx%d is over %s pixels; lens doesn't decode images this big (it would need about %.0f GB of memory)",
			cfg.Width, cfg.Height, count(maxPixels), float64(cfg.Width)*float64(cfg.Height)*8/1e9)
	}
	var m image.Image
	if kind == kGIF {
		g, err := gif.DecodeAll(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("can't decode it as GIF: %v", err)
		}
		m = gifFirstFrame(g)
		gifFacts(f, g)
	} else {
		m, _, err = image.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("can't decode it as %s: %v", kind, err)
		}
	}
	f.img = toNRGBA(m)
	f.w, f.h = f.img.Rect.Dx(), f.img.Rect.Dy()
	if f.stored == "" {
		f.stored = goColorType(m)
	}
	return f, nil
}

// maxPixels is the most lens decodes: 16384x16384, about 2 GB at work.
const maxPixels = 16384 * 16384

// tiffFacts reads the first directory: the photometric kind, the bits a
// sample, and whether an extra sample is alpha.
func tiffFacts(f *imageFacts) {
	b := f.raw
	if len(b) < 8 {
		return
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if b[0] == 'M' {
		bo = binary.BigEndian
	}
	off := int(bo.Uint32(b[4:8]))
	if off < 8 || off+2 > len(b) {
		return
	}
	photo, bits, alpha := -1, 1, false
	for i := 0; i < int(bo.Uint16(b[off:])); i++ {
		e := off + 2 + 12*i
		if e+12 > len(b) {
			break
		}
		tag, typ, cnt := bo.Uint16(b[e:]), bo.Uint16(b[e+2:]), bo.Uint32(b[e+4:])
		first := func() int {
			switch {
			case typ == 3 && cnt <= 2:
				return int(bo.Uint16(b[e+8:]))
			case typ == 3:
				if p := int(bo.Uint32(b[e+8:])); p >= 0 && p+2 <= len(b) {
					return int(bo.Uint16(b[p:]))
				}
			case typ == 4 && cnt <= 1:
				return int(bo.Uint32(b[e+8:]))
			}
			return 0
		}
		switch tag {
		case 258:
			bits = first()
		case 262:
			photo = first()
		case 338: // ExtraSamples: 1 associated alpha, 2 unassociated
			v := first()
			alpha = v == 1 || v == 2
		}
	}
	names := map[int]string{0: "gray", 1: "gray", 2: "RGB", 3: "palette", 5: "CMYK", 6: "YCbCr"}
	name, ok := names[photo]
	if !ok {
		return
	}
	if alpha {
		name = map[string]string{"gray": "gray+alpha", "RGB": "RGBA"}[name]
		if name == "" {
			name = names[photo] + " with alpha"
		}
	}
	f.stored = fmt.Sprintf("%s %d-bit", name, bits)
	f.canAlpha = alpha
}

// pngFacts reads IHDR and the chunks that matter: tRNS, PLTE, acTL, fcTL.
func pngFacts(f *imageFacts) {
	b := f.raw
	if len(b) < 33 {
		return
	}
	depth, ct, interlace := b[24], b[25], b[28]
	names := map[byte]string{0: "gray", 2: "RGB", 3: "palette", 4: "gray+alpha", 6: "RGBA"}
	f.stored = fmt.Sprintf("%s %d-bit", names[ct], depth)
	f.canAlpha = ct == 4 || ct == 6
	if interlace == 1 {
		f.extra = "interlaced"
	}
	trns, plte := false, 0
	var frames int
	var dur float64
	for off := 8; off+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[off : off+4]))
		typ := string(b[off+4 : off+8])
		data := off + 8
		if n < 0 || data+n > len(b) {
			break
		}
		switch typ {
		case "PLTE":
			plte = n / 3
		case "tRNS":
			trns = true
		case "acTL":
			if n >= 4 {
				frames = int(binary.BigEndian.Uint32(b[data : data+4]))
			}
		case "fcTL":
			if n >= 26 {
				num := float64(binary.BigEndian.Uint16(b[data+20 : data+22]))
				den := float64(binary.BigEndian.Uint16(b[data+22 : data+24]))
				if den == 0 {
					den = 100
				}
				dur += num / den
			}
		case "IEND":
			off = len(b)
			continue
		}
		off = data + n + 4
	}
	if ct == 3 && plte > 0 {
		f.stored = fmt.Sprintf("palette of %s", plural(plte, "color"))
	}
	if trns {
		f.canAlpha = true
		f.stored += " with tRNS transparency"
	}
	if frames > 1 {
		f.frames = frames
		f.dur = time.Duration(dur * float64(time.Second))
		f.kind = "APNG"
	}
}

// jpegFacts reads the frame marker: baseline or progressive, gray or color.
func jpegFacts(f *imageFacts) {
	b := f.raw
	for off := 2; off+4 <= len(b); {
		if b[off] != 0xff {
			off++
			continue
		}
		m := b[off+1]
		if m == 0xff || m == 0x01 || (m >= 0xd0 && m <= 0xd8) {
			off += 2
			continue
		}
		n := int(binary.BigEndian.Uint16(b[off+2 : off+4]))
		if m >= 0xc0 && m <= 0xcf && m != 0xc4 && m != 0xc8 && m != 0xcc && off+9 < len(b) {
			comps := b[off+9]
			switch comps {
			case 1:
				f.stored = "gray 8-bit"
			case 3:
				f.stored = "YCbCr 8-bit"
			case 4:
				f.stored = "CMYK 8-bit"
			}
			if m == 0xc2 || m == 0xc6 || m == 0xca || m == 0xce {
				f.extra = "progressive"
			}
			return
		}
		off += 2 + n
	}
}

// webpFacts reads the RIFF chunks: lossy or lossless, alpha, animation.
func webpFacts(f *imageFacts) error {
	b := f.raw
	if len(b) < 16 {
		return nil
	}
	switch string(b[12:16]) {
	case "VP8 ":
		f.stored, f.lossy = "lossy", true
	case "VP8L":
		f.stored = "lossless"
		if len(b) >= 25 {
			f.canAlpha = b[24]&0x10 != 0 // alpha_is_used, in the 4 bytes after the size
		}
	case "VP8X":
		if len(b) < 30 {
			return nil
		}
		flags := b[20]
		f.canAlpha = flags&0x10 != 0
		if flags&0x02 != 0 {
			w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
			h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
			return fmt.Errorf("an animated WebP, %dx%d; lens reads still WebP images only", w+1, h+1)
		}
		f.stored = "lossy"
		f.lossy = true
		if bytes.Contains(b[:min(len(b), 4096)], []byte("VP8L")) {
			f.stored, f.lossy = "lossless", false
		}
	}
	return nil
}

func gifFacts(f *imageFacts, g *gif.GIF) {
	if len(g.Image) > 1 {
		f.frames = len(g.Image)
		cs := 0
		for _, d := range g.Delay {
			cs += d
		}
		f.dur = time.Duration(cs) * 10 * time.Millisecond
	}
	pal := 0
	for _, fr := range g.Image {
		pal = max(pal, len(fr.Palette))
		for _, c := range fr.Palette {
			if _, _, _, a := c.RGBA(); a < 0xffff {
				f.canAlpha = true
			}
		}
	}
	f.stored = fmt.Sprintf("palette of %s", plural(pal, "color"))
}

// gifFirstFrame draws the first frame on the logical screen, as a viewer
// shows it.
func gifFirstFrame(g *gif.GIF) image.Image {
	if len(g.Image) == 0 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	fr := g.Image[0]
	w, h := g.Config.Width, g.Config.Height
	if w == 0 || h == 0 || (fr.Rect.Min == image.Point{} && fr.Rect.Dx() == w && fr.Rect.Dy() == h) {
		return fr
	}
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(m, fr.Rect, fr, fr.Rect.Min, draw.Over)
	return m
}

// goColorType names a decoded image's color type, for formats whose header
// lens doesn't read.
func goColorType(m image.Image) string {
	switch m.(type) {
	case *image.Gray:
		return "gray 8-bit"
	case *image.Gray16:
		return "gray 16-bit"
	case *image.NRGBA, *image.RGBA:
		return "RGBA 8-bit"
	case *image.NRGBA64, *image.RGBA64:
		return "RGBA 16-bit"
	case *image.YCbCr:
		return "YCbCr 8-bit"
	case *image.CMYK:
		return "CMYK 8-bit"
	case *image.Paletted:
		return fmt.Sprintf("palette of %s", plural(len(m.(*image.Paletted).Palette), "color"))
	}
	return fmt.Sprintf("%T", m)
}

// toNRGBA gives the image as 8-bit non-premultiplied RGBA at the origin.
func toNRGBA(m image.Image) *image.NRGBA {
	b := m.Bounds()
	r := image.Rect(0, 0, b.Dx(), b.Dy())
	switch s := m.(type) {
	case *image.NRGBA:
		if s.Rect.Min == (image.Point{}) && s.Stride == 4*b.Dx() {
			return s
		}
	case *image.YCbCr, *image.Gray:
		// opaque: drawn fast as RGBA, which is then the same as NRGBA
		d := image.NewRGBA(r)
		draw.Draw(d, r, m, b.Min, draw.Src)
		return &image.NRGBA{Pix: d.Pix, Stride: d.Stride, Rect: r}
	case *image.RGBA:
		d := image.NewNRGBA(r)
		for y := 0; y < r.Dy(); y++ {
			src := s.Pix[s.PixOffset(b.Min.X, b.Min.Y+y):]
			dst := d.Pix[y*d.Stride:]
			for x := 0; x < r.Dx(); x++ {
				i := 4 * x
				a := src[i+3]
				switch a {
				case 255:
					copy(dst[i:i+4], src[i:i+4])
				case 0:
					// fully clear: color 0
				default:
					dst[i] = uint8(uint32(src[i]) * 255 / uint32(a))
					dst[i+1] = uint8(uint32(src[i+1]) * 255 / uint32(a))
					dst[i+2] = uint8(uint32(src[i+2]) * 255 / uint32(a))
					dst[i+3] = a
				}
			}
		}
		return d
	case *image.Paletted:
		pal := make([]color.NRGBA, 256)
		for i, c := range s.Palette {
			pal[i] = color.NRGBAModel.Convert(c).(color.NRGBA)
		}
		d := image.NewNRGBA(r)
		for y := 0; y < r.Dy(); y++ {
			src := s.Pix[s.PixOffset(b.Min.X, b.Min.Y+y):]
			dst := d.Pix[y*d.Stride:]
			for x := 0; x < r.Dx(); x++ {
				c := pal[src[x]]
				dst[4*x], dst[4*x+1], dst[4*x+2], dst[4*x+3] = c.R, c.G, c.B, c.A
			}
		}
		return d
	}
	d := image.NewNRGBA(r)
	draw.Draw(d, r, m, b.Min, draw.Src)
	return d
}
