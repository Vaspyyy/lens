package main

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lensRun runs lens with args and gives its output and exit code.
func lensRun(t *testing.T, args ...string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	code := run(args, w)
	w.Flush()
	return buf.String(), code
}

func mustContain(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

func mustLack(t *testing.T, out string, bad ...string) {
	t.Helper()
	for _, s := range bad {
		if strings.Contains(out, s) {
			t.Errorf("output has %q:\n%s", s, out)
		}
	}
}

// savePNG writes m as a PNG in dir.
func savePNG(t *testing.T, dir, name string, m image.Image) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, m); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

// canvas is an NRGBA image filled with c.
func canvas(w, h int, c color.NRGBA) *image.NRGBA {
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetNRGBA(x, y, c)
		}
	}
	return m
}

func rect(m *image.NRGBA, r image.Rectangle, c color.NRGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			m.SetNRGBA(x, y, c)
		}
	}
}

// opaqueRGB is an image Go's PNG encoder writes without an alpha channel.
func opaqueRGB(m *image.NRGBA) *image.RGBA {
	r := image.NewRGBA(m.Rect)
	copy(r.Pix, m.Pix)
	return r
}

// wavSpec describes a WAV file to write.
type wavSpec struct {
	rate, bits int
	float      bool
	extensible bool
	dataSize   int64 // -1: the real size; else written as is
}

// writeWAV writes channels of samples (each -1..1, or beyond for float).
func writeWAV(t *testing.T, path string, spec wavSpec, chans ...[]float64) {
	t.Helper()
	if spec.rate == 0 {
		spec.rate = 44100
	}
	if spec.bits == 0 {
		spec.bits = 16
	}
	ch := len(chans)
	n := len(chans[0])
	bps := spec.bits / 8
	var data bytes.Buffer
	for i := 0; i < n; i++ {
		for c := 0; c < ch; c++ {
			x := chans[c][i]
			switch {
			case spec.float && bps == 4:
				binary.Write(&data, binary.LittleEndian, math.Float32bits(float32(x)))
			case spec.float && bps == 8:
				binary.Write(&data, binary.LittleEndian, math.Float64bits(x))
			case bps == 1:
				data.WriteByte(uint8(clampInt(int(math.Round(x*128))+128, 0, 255)))
			case bps == 2:
				binary.Write(&data, binary.LittleEndian, int16(clampInt(int(math.Round(x*32768)), -32768, 32767)))
			case bps == 3:
				v := clampInt(int(math.Round(x*8388608)), -8388608, 8388607)
				data.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16)})
			case bps == 4:
				v := int64(math.Round(x * 2147483648))
				v = max(-2147483648, min(2147483647, v))
				binary.Write(&data, binary.LittleEndian, int32(v))
			}
		}
	}
	tag := uint16(1)
	if spec.float {
		tag = 3
	}
	var fmtc bytes.Buffer
	ftag := tag
	if spec.extensible {
		ftag = 0xfffe
	}
	binary.Write(&fmtc, binary.LittleEndian, ftag)
	binary.Write(&fmtc, binary.LittleEndian, uint16(ch))
	binary.Write(&fmtc, binary.LittleEndian, uint32(spec.rate))
	binary.Write(&fmtc, binary.LittleEndian, uint32(spec.rate*ch*bps))
	binary.Write(&fmtc, binary.LittleEndian, uint16(ch*bps))
	binary.Write(&fmtc, binary.LittleEndian, uint16(spec.bits))
	if spec.extensible {
		binary.Write(&fmtc, binary.LittleEndian, uint16(22))
		binary.Write(&fmtc, binary.LittleEndian, uint16(spec.bits))
		binary.Write(&fmtc, binary.LittleEndian, uint32(0))
		binary.Write(&fmtc, binary.LittleEndian, tag)
		fmtc.Write([]byte("\x00\x00\x00\x00\x10\x00\x80\x00\x00\xaa\x00\x38\x9b\x71"))
	}
	var out bytes.Buffer
	out.WriteString("RIFF")
	binary.Write(&out, binary.LittleEndian, uint32(4+8+fmtc.Len()+8+data.Len()))
	out.WriteString("WAVE")
	out.WriteString("LIST") // a chunk to skip
	binary.Write(&out, binary.LittleEndian, uint32(3))
	out.Write([]byte{1, 2, 3, 0})
	out.WriteString("fmt ")
	binary.Write(&out, binary.LittleEndian, uint32(fmtc.Len()))
	out.Write(fmtc.Bytes())
	out.WriteString("data")
	size := int64(data.Len())
	if spec.dataSize != 0 {
		size = spec.dataSize
	}
	binary.Write(&out, binary.LittleEndian, uint32(size))
	out.Write(data.Bytes())
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func clampInt(v, lo, hi int) int { return max(lo, min(hi, v)) }

// rawRGBAPNG writes m as a PNG with color type 6, even when it is opaque.
func rawRGBAPNG(m *image.NRGBA) []byte {
	chunk := func(out *bytes.Buffer, typ string, data []byte) {
		binary.Write(out, binary.BigEndian, uint32(len(data)))
		c := crc32.NewIEEE()
		c.Write([]byte(typ))
		c.Write(data)
		out.WriteString(typ)
		out.Write(data)
		binary.Write(out, binary.BigEndian, c.Sum32())
	}
	var out bytes.Buffer
	out.WriteString("\x89PNG\r\n\x1a\n")
	var ihdr bytes.Buffer
	binary.Write(&ihdr, binary.BigEndian, uint32(m.Rect.Dx()))
	binary.Write(&ihdr, binary.BigEndian, uint32(m.Rect.Dy()))
	ihdr.Write([]byte{8, 6, 0, 0, 0})
	chunk(&out, "IHDR", ihdr.Bytes())
	var raw bytes.Buffer
	for y := 0; y < m.Rect.Dy(); y++ {
		raw.WriteByte(0)
		raw.Write(m.Pix[y*m.Stride : y*m.Stride+4*m.Rect.Dx()])
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(raw.Bytes())
	zw.Close()
	chunk(&out, "IDAT", z.Bytes())
	chunk(&out, "IEND", nil)
	return out.Bytes()
}

// sine is seconds of a tone at a peak level in dBFS.
func sine(rate int, freq, secs, db float64) []float64 {
	a := math.Pow(10, db/20)
	out := make([]float64, int(secs*float64(rate)))
	for i := range out {
		out[i] = a * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))
	}
	return out
}

func silence(rate int, secs float64) []float64 { return make([]float64, int(secs*float64(rate))) }

func cat(parts ...[]float64) []float64 {
	var out []float64
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, c := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skip(c + " isn't installed")
		}
	}
}
