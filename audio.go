package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hajimehoshi/go-mp3"
	"github.com/jfreymuth/oggvorbis"
)

// pcmReader gives decoded audio as interleaved float32 samples.
type pcmReader interface {
	Read(p []float32) (int, error) // a whole number of frames
	Close() error
}

// audioInfo is what the container says before decoding.
type audioInfo struct {
	format   string // "WAV", "MP3", "Ogg Vorbis", "Ogg Opus", "FLAC"
	detail   string // "PCM 16-bit", "float 32-bit"
	rate, ch int
	size     int64
	lossy    bool    // a bitrate is worth showing
	bits     int     // integer PCM's bits a sample, for where full scale is
	audio    int64   // bytes of sound without tags and cover art, when known
	warn     string  // what's wrong with the file around the sound
	mask     uint32  // WAV's channel mask: which speaker each channel is
	layout   string  // ffmpeg's channel layout: "5.1(side)", "2.1", "FL+FR+LFE"
	expect   float64 // seconds of sound a header promises, when ffmpeg decodes
}

// maxChannels is the most channels lens takes a file to have.
const maxChannels = 64

var errUnsupported = errors.New("unsupported")

// openAudio opens a sound for decoding: WAV, MP3 and Ogg Vorbis in Go,
// everything else through ffmpeg.
func openAudio(path string, kind fileKind) (pcmReader, audioInfo, error) {
	var size int64
	if fi, err := os.Stat(path); err == nil {
		size = fi.Size()
	}
	var r pcmReader
	var info audioInfo
	var err error
	var oggWarn string
	var starts []int64
	if kind == kVorbis || kind == kOgg {
		starts, oggWarn = oggPages(path)
	}
	switch kind {
	case kWAV:
		r, info, err = openWAV(path)
	case kMP3:
		r, info, err = openMP3(path)
	case kVorbis:
		r, info, err = openVorbis(path, starts)
	default:
		err = errUnsupported
	}
	if errors.Is(err, errUnsupported) {
		expect := info.expect // what the header promised, for after ffmpeg
		r, info, err = openFFmpeg(path, kind, err)
		if info.expect == 0 {
			info.expect = expect
		}
	}
	if err != nil && oggWarn != "" { // say what's wrong with the file first
		err = fmt.Errorf("%s; %v", oggWarn, err)
	}
	info.size = size
	if oggWarn != "" {
		info.warn = oggWarn
	}
	return r, info, err
}

func haveFFmpeg() bool {
	_, e1 := exec.LookPath("ffmpeg")
	_, e2 := exec.LookPath("ffprobe")
	return e1 == nil && e2 == nil
}

// --- WAV

type wavReader struct {
	f      *os.File
	r      *bufio.Reader
	format int // 1 integer PCM, 3 float
	bits   int
	ch     int
	left   int64 // bytes left in the data chunk; -1: up to the end of the file
	buf    []byte

	declared int64 // the data chunk's size, -1 unknown
	rate     int
	byteRate int
}

// chunkNext: the next bytes start a RIFF chunk (four letters), so an empty
// data chunk is really empty and not a size left at 0.
func chunkNext(r *bufio.Reader) bool {
	b, _ := r.Peek(8)
	if len(b) < 8 {
		return true // nothing after it: empty either way
	}
	for _, c := range b[:4] {
		if c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

// truncated says how much of the promised sound the file holds.
func (w *wavReader) truncated() error {
	frame := float64(w.bits / 8 * w.ch * w.rate)
	return fmt.Errorf("the file ends early: its header promises %s of sound, the file holds %s",
		secText(float64(w.declared)/frame), secText(float64(w.declared-w.left)/frame))
}

func openWAV(path string) (pcmReader, audioInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, audioInfo{}, err
	}
	w, info, err := readWAVHeader(f)
	if err != nil {
		f.Close()
		return nil, info, err
	}
	return w, info, nil
}

func readWAVHeader(f *os.File) (*wavReader, audioInfo, error) {
	r := bufio.NewReaderSize(f, 1<<16)
	info := audioInfo{format: "WAV"}
	head := make([]byte, 12)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, info, fmt.Errorf("a WAV file too short for its header")
	}
	rf64 := string(head[:4]) == "RF64"
	riffSize := binary.LittleEndian.Uint32(head[4:8])
	w := &wavReader{f: f, r: r, left: -1}
	var ds64 int64 = -1
	haveFmt := false
	for {
		ch := make([]byte, 8)
		if _, err := io.ReadFull(r, ch); err != nil {
			return nil, info, fmt.Errorf("a WAV file with no data chunk")
		}
		id := string(ch[:4])
		n := int64(binary.LittleEndian.Uint32(ch[4:]))
		switch id {
		case "fmt ":
			if n < 16 || n > 1<<12 {
				return nil, info, fmt.Errorf("a WAV file with a broken fmt chunk (%s long)", plural(int(n), "byte"))
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, info, fmt.Errorf("a WAV file with a broken fmt chunk")
			}
			tag := int(binary.LittleEndian.Uint16(b[0:2]))
			w.ch = int(binary.LittleEndian.Uint16(b[2:4]))
			info.rate = int(binary.LittleEndian.Uint32(b[4:8]))
			w.byteRate = int(binary.LittleEndian.Uint32(b[8:12]))
			w.bits = int(binary.LittleEndian.Uint16(b[14:16]))
			if tag == 0xfffe && n >= 26 {
				info.mask = binary.LittleEndian.Uint32(b[20:24])
				tag = int(binary.LittleEndian.Uint16(b[24:26]))
			}
			w.format = tag
			haveFmt = true
			if n%2 == 1 {
				r.Discard(1)
			}
		case "ds64":
			if n > 1<<12 {
				return nil, info, fmt.Errorf("a WAV file with a broken ds64 chunk")
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, info, fmt.Errorf("a WAV file with a broken ds64 chunk")
			}
			if n >= 16 {
				ds64 = int64(binary.LittleEndian.Uint64(b[8:16]))
			}
		case "data":
			if !haveFmt {
				return nil, info, fmt.Errorf("a WAV file with its data before its fmt chunk")
			}
			switch {
			case rf64 && ds64 >= 0:
				w.left = ds64
			case n == 0xffffffff, n >= 0x7fff0000:
				w.left = -1 // a placeholder, as a WAV written to a pipe has
			case n == 0 && (riffSize == 0 || riffSize == 0xffffffff || !chunkNext(r)):
				w.left = -1 // a writer stopped before it fixed the sizes
			default:
				w.left = n // 0 too: an empty data chunk is empty
			}
			w.declared = w.left
			w.rate = info.rate
			info.ch = w.ch
			switch {
			case w.format == 1 && (w.bits == 8 || w.bits == 16 || w.bits == 24 || w.bits == 32):
				info.detail = fmt.Sprintf("PCM %d-bit", w.bits)
				info.bits = w.bits
			case w.format == 3 && (w.bits == 32 || w.bits == 64):
				info.detail = fmt.Sprintf("float %d-bit", w.bits)
			default:
				names := map[int]string{2: "MS ADPCM", 6: "A-law", 7: "µ-law", 0x11: "IMA ADPCM", 0x55: "MP3"}
				name := names[w.format]
				if name == "" {
					name = fmt.Sprintf("format %d, %d-bit", w.format, w.bits)
				}
				if w.left > 0 && w.byteRate > 0 { // ffmpeg decodes it; the header says how long
					info.expect = float64(w.left) / float64(w.byteRate)
				}
				return nil, info, fmt.Errorf("%w: WAV with %s", errUnsupported, name)
			}
			if w.ch < 1 || w.ch > maxChannels || info.rate < 1 {
				return nil, info, fmt.Errorf("a WAV file with %d channels at %d Hz, which lens doesn't read (up to %d channels)", w.ch, info.rate, maxChannels)
			}
			return w, info, nil
		default:
			if _, err := r.Discard(int(n + n%2)); err != nil {
				return nil, info, fmt.Errorf("a WAV file with no data chunk")
			}
		}
	}
}

func (w *wavReader) Read(p []float32) (int, error) {
	bps := w.bits / 8
	frame := bps * w.ch
	n := len(p) / w.ch * frame
	if w.left >= 0 && int64(n) > w.left {
		n = int(w.left) / frame * frame
	}
	if n == 0 && w.left > 0 { // less than a frame is left of the data
		rest := make([]byte, w.left)
		got, _ := io.ReadFull(w.r, rest)
		w.left -= int64(got)
		if w.left > 0 {
			return 0, w.truncated()
		}
		return 0, fmt.Errorf("the data chunk ends in a partial sample frame (%s left over)", plural(got, "byte"))
	}
	if n == 0 {
		return 0, io.EOF
	}
	if cap(w.buf) < n {
		w.buf = make([]byte, n)
	}
	b := w.buf[:n]
	got, err := io.ReadFull(w.r, b)
	got = got / frame * frame
	if w.left >= 0 {
		w.left -= int64(got)
	}
	b = b[:got]
	k := 0
	switch {
	case w.format == 3 && bps == 4:
		for i := 0; i+4 <= len(b); i += 4 {
			p[k] = math.Float32frombits(binary.LittleEndian.Uint32(b[i:]))
			k++
		}
	case w.format == 3 && bps == 8:
		for i := 0; i+8 <= len(b); i += 8 {
			p[k] = float32(math.Float64frombits(binary.LittleEndian.Uint64(b[i:])))
			k++
		}
	case bps == 1:
		for _, v := range b {
			p[k] = (float32(v) - 128) / 128
			k++
		}
	case bps == 2:
		for i := 0; i+2 <= len(b); i += 2 {
			p[k] = float32(int16(binary.LittleEndian.Uint16(b[i:]))) / 32768
			k++
		}
	case bps == 3:
		for i := 0; i+3 <= len(b); i += 3 {
			v := int32(uint32(b[i])<<8|uint32(b[i+1])<<16|uint32(b[i+2])<<24) >> 8
			p[k] = float32(v) / 8388608
			k++
		}
	case bps == 4:
		for i := 0; i+4 <= len(b); i += 4 {
			p[k] = float32(float64(int32(binary.LittleEndian.Uint32(b[i:]))) / 2147483648)
			k++
		}
	}
	if (err == io.ErrUnexpectedEOF || err == io.EOF) && w.left > 0 {
		return k, w.truncated()
	}
	if err == io.ErrUnexpectedEOF || (err == io.EOF && k > 0) {
		err = nil
	}
	if k == 0 && err == nil {
		err = io.EOF
	}
	return k, err
}

func (w *wavReader) Close() error { return w.f.Close() }

// --- MP3

type mp3Reader struct {
	f    *os.File
	d    *mp3.Decoder
	mono bool
	skip int64 // frames still to drop at the start
	left int64 // frames still to give; -1: all
	buf  []byte
	done int64 // frames given
	rate int
}

func openMP3(path string) (pcmReader, audioInfo, error) {
	info := audioInfo{format: "MP3", lossy: true}
	raw := make([]byte, 64<<10)
	f, err := os.Open(path)
	if err != nil {
		return nil, info, err
	}
	n, _ := io.ReadFull(f, raw[:10])
	tag := id3Size(raw[:n]) // cover art can make the tag bigger than any sample
	if _, err := f.Seek(int64(tag), io.SeekStart); err != nil {
		f.Close()
		return nil, info, err
	}
	n, _ = io.ReadFull(f, raw)
	h := parseMP3Header(raw[:n])
	if fi, err := f.Stat(); err == nil {
		info.audio = fi.Size() - int64(tag)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, info, err
	}
	if !h.mpeg1 { // go-mp3 gets MPEG-2 and 2.5 wrong or not at all: ffmpeg
		f.Close()
		if h.xing && h.frames > 0 && h.rate > 0 { // the length the header promises
			n := h.frames * h.spf
			if h.lame {
				n -= h.delay + h.padding
			}
			info.expect = float64(n) / float64(h.rate)
		}
		return nil, info, fmt.Errorf("%w: this MP3 (MPEG-2 or 2.5, which needs ffmpeg)", errUnsupported)
	}
	d, err := mp3.NewDecoder(bufio.NewReaderSize(f, 1<<16))
	if err != nil {
		f.Close()
		return nil, info, fmt.Errorf("%w: this MP3 (%v)", errUnsupported, err) // MPEG 2.5: ffmpeg
	}
	info.rate = d.SampleRate()
	info.ch = 2
	if h.mono {
		info.ch = 1
	}
	m := &mp3Reader{f: f, d: d, mono: h.mono, left: -1, rate: info.rate}
	// go-mp3 decodes the Xing/Info frame as silence and doesn't remove the
	// encoder's delay and padding, which LAME's tag records
	if h.xing {
		m.skip = int64(h.spf)
		if h.lame {
			m.skip += int64(h.delay) + 529
			if h.frames > 0 {
				m.left = int64(h.frames)*int64(h.spf) - int64(h.delay) - int64(h.padding)
			}
		}
	}
	return m, info, nil
}

func (m *mp3Reader) Read(p []float32) (int, error) {
	ch := 2
	if m.mono {
		ch = 1
	}
	frames := len(p) / ch
	if m.left >= 0 && int64(frames) > m.left {
		frames = int(m.left)
	}
	if frames == 0 {
		return 0, io.EOF
	}
	for {
		want := frames * 4
		if m.skip > 0 {
			want = int(min(m.skip, int64(len(p)/ch))) * 4
		}
		if cap(m.buf) < want {
			m.buf = make([]byte, want)
		}
		b := m.buf[:want]
		got, err := io.ReadFull(m.d, b)
		got = got / 4 * 4
		if m.skip > 0 {
			m.skip -= int64(got / 4)
			if err != nil {
				return 0, io.EOF
			}
			continue
		}
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return 0, fmt.Errorf("can't decode it past %s: %v", secText(float64(m.done)/float64(m.rate)), err)
		}
		if got == 0 && m.left >= 576 { // half a frame short or more: frames went missing
			return 0, fmt.Errorf("the file ends early: its LAME header promises %s of sound, the file holds %s",
				secText(float64(m.done+m.left)/float64(m.rate)), secText(float64(m.done)/float64(m.rate)))
		}
		k := 0
		for i := 0; i+4 <= got; i += 4 {
			l := float32(int16(binary.LittleEndian.Uint16(b[i:]))) / 32768
			p[k] = l
			k++
			if !m.mono {
				p[k] = float32(int16(binary.LittleEndian.Uint16(b[i+2:]))) / 32768
				k++
			}
		}
		if m.left >= 0 {
			m.left -= int64(got / 4)
		}
		m.done += int64(got / 4)
		if k == 0 {
			return 0, io.EOF
		}
		return k, nil
	}
}

func (m *mp3Reader) Close() error { return m.f.Close() }

type mp3Header struct {
	mono           bool
	mpeg1          bool
	rate           int
	spf            int // samples a frame
	xing, lame     bool
	frames         int
	delay, padding int
}

// id3Size is the length of an ID3v2 tag at the start of b, or 0.
func id3Size(b []byte) int {
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return 0
	}
	n := 10 + (int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f))
	if b[5]&0x10 != 0 { // a footer
		n += 10
	}
	return n
}

// parseMP3Header reads the first frame: channels, and the Xing/Info and
// LAME tags with the frame count, encoder delay and padding.
func parseMP3Header(b []byte) mp3Header {
	h := mp3Header{spf: 1152}
	i := id3Size(b)
	for ; i+4 <= len(b); i++ {
		if b[i] == 0xff && b[i+1]&0xe0 == 0xe0 && mp3Frame(b[i:]) {
			break
		}
	}
	if i+4 > len(b) {
		return h
	}
	ver := (b[i+1] >> 3) & 3 // 3: MPEG-1
	h.mono = b[i+3]>>6 == 3
	h.mpeg1 = ver == 3
	rates := map[byte][3]int{3: {44100, 48000, 32000}, 2: {22050, 24000, 16000}, 0: {11025, 12000, 8000}}
	if ri := (b[i+2] >> 2) & 3; ri < 3 {
		h.rate = rates[ver][ri]
	}
	side := 32
	switch {
	case ver == 3 && h.mono:
		side = 17
	case ver != 3 && h.mono:
		side = 9
	case ver != 3:
		side = 17
	}
	if ver != 3 {
		h.spf = 576
	}
	x := i + 4 + side
	if x+8 > len(b) || (string(b[x:x+4]) != "Xing" && string(b[x:x+4]) != "Info") {
		return h
	}
	h.xing = true
	flags := binary.BigEndian.Uint32(b[x+4:])
	o := x + 8
	if flags&1 != 0 && o+4 <= len(b) {
		h.frames = int(binary.BigEndian.Uint32(b[o:]))
		o += 4
	}
	if flags&2 != 0 {
		o += 4
	}
	if flags&4 != 0 {
		o += 100
	}
	if flags&8 != 0 {
		o += 4
	}
	if o+24 <= len(b) {
		tag := string(b[o : o+4])
		if tag == "LAME" || tag == "Lavc" || tag == "Lavf" || tag == "L3.9" || tag == "GOGO" {
			h.lame = true
			d := b[o+21:]
			h.delay = int(d[0])<<4 | int(d[1])>>4
			h.padding = int(d[1]&0x0f)<<8 | int(d[2])
		}
	}
	return h
}

// --- Ogg Vorbis

// vorbisReader decodes Ogg Vorbis, chained files one stream after another
// (each with its own setup, which ffmpeg gets wrong when it changes).
type vorbisReader struct {
	f      *os.File
	r      *oggvorbis.Reader
	starts []int64 // where each chained stream begins
	next   int     // the stream to open when this one ends
	size   int64
	done   int64 // samples given, for a message
}

func (v *vorbisReader) open(i int) (*oggvorbis.Reader, error) {
	end := v.size
	if i+1 < len(v.starts) {
		end = v.starts[i+1]
	}
	start := int64(0)
	if i < len(v.starts) {
		start = v.starts[i]
	}
	return oggvorbis.NewReader(bufio.NewReaderSize(io.NewSectionReader(v.f, start, end-start), 1<<16))
}

func openVorbis(path string, starts []int64) (pcmReader, audioInfo, error) {
	info := audioInfo{format: "Ogg Vorbis", lossy: true}
	f, err := os.Open(path)
	if err != nil {
		return nil, info, err
	}
	v := &vorbisReader{f: f, starts: starts, next: 1}
	if fi, err := f.Stat(); err == nil {
		v.size = fi.Size()
	}
	v.r, err = v.open(0)
	if err != nil {
		f.Close()
		return nil, info, fmt.Errorf("%w: this Ogg Vorbis file (%v)", errUnsupported, err)
	}
	info.rate, info.ch = v.r.SampleRate(), v.r.Channels()
	// the channels come out in WAV's order (vorbisOrder); say which speakers
	info.mask = map[int]uint32{3: 0x7, 4: 0x33, 5: 0x37, 6: 0x3f, 7: 0x70f, 8: 0x63f}[info.ch]
	return v, info, nil
}

// vorbisOrder maps Vorbis's channel order to WAV's (front left, front
// right, center, LFE, back, side), for 3 to 8 channels.
var vorbisOrder = map[int][]int{
	3: {0, 2, 1},
	5: {0, 2, 1, 3, 4},
	6: {0, 2, 1, 5, 3, 4},
	7: {0, 2, 1, 6, 5, 3, 4},
	8: {0, 2, 1, 7, 5, 6, 3, 4},
}

func (v *vorbisReader) Read(p []float32) (int, error) {
	n, err := v.r.Read(p)
	for n == 0 && err == io.EOF && v.next < len(v.starts) { // the next chained stream
		rate, ch := v.r.SampleRate(), v.r.Channels()
		nr, oerr := v.open(v.next)
		v.next++
		if oerr != nil {
			return 0, fmt.Errorf("chained stream %d of %d can't be decoded: %v", v.next, len(v.starts), oerr)
		}
		if nr.SampleRate() != rate || nr.Channels() != ch {
			return 0, fmt.Errorf("chained stream %d of %d has %s %s, the first %s %s; lens stops there",
				v.next, len(v.starts), rateText(nr.SampleRate()), channelsText(nr.Channels()), rateText(rate), channelsText(ch))
		}
		v.r = nr
		n, err = v.r.Read(p)
	}
	if err == io.EOF && n > 0 && v.next < len(v.starts) {
		err = nil // the last samples of this stream; the next one follows
	}
	ch := v.r.Channels()
	if order, ok := vorbisOrder[ch]; ok {
		tmp := make([]float32, ch)
		for i := 0; i+ch <= n; i += ch {
			for j, from := range order {
				tmp[j] = p[i+from]
			}
			copy(p[i:i+ch], tmp)
		}
	}
	return n, err
}

func (v *vorbisReader) Close() error { return v.f.Close() }

// oggPages walks an Ogg file's pages, checking each one's checksum: how
// many logical streams start in it, and what's wrong with it, if anything.
func oggPages(path string) ([]int64, string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	var off int64
	var starts []int64 // where each logical stream begins
	ended := false
	page := make([]byte, 27+255+255*255)
	for {
		n, err := io.ReadFull(r, page[:27])
		if n == 0 && err == io.EOF {
			break
		}
		if err != nil {
			return starts, fmt.Sprintf("the file ends early: it stops inside an Ogg page at byte %s", count(int(off)))
		}
		if string(page[:4]) != "OggS" {
			return starts, fmt.Sprintf("the file is damaged: no Ogg page where one should start, at byte %s", count(int(off)))
		}
		segs := int(page[26])
		if _, err := io.ReadFull(r, page[27:27+segs]); err != nil {
			return starts, fmt.Sprintf("the file ends early: it stops inside an Ogg page at byte %s", count(int(off)))
		}
		body := 0
		for _, s := range page[27 : 27+segs] {
			body += int(s)
		}
		size := 27 + segs + body
		if _, err := io.ReadFull(r, page[27+segs:size]); err != nil {
			return starts, fmt.Sprintf("the file ends early: it stops inside an Ogg page at byte %s", count(int(off)))
		}
		want := binary.LittleEndian.Uint32(page[22:26])
		if oggCRC(page[:size]) != want {
			return starts, fmt.Sprintf("the file is damaged: the Ogg page at byte %s fails its checksum", count(int(off)))
		}
		if page[5]&2 != 0 {
			starts = append(starts, off)
		}
		ended = page[5]&4 != 0
		off += int64(size)
	}
	if !ended && off > 0 {
		return starts, "the file may be cut short: its last Ogg page doesn't end the stream"
	}
	return starts, ""
}

var oggTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		c := uint32(i) << 24
		for k := 0; k < 8; k++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// oggCRC is the page checksum, computed with its own field as zeros.
func oggCRC(page []byte) uint32 {
	var c uint32
	for i, b := range page {
		if i >= 22 && i < 26 {
			b = 0
		}
		c = c<<8 ^ oggTable[byte(c>>24)^b]
	}
	return c
}

// --- ffmpeg

type ffReader struct {
	cmd    *exec.Cmd
	out    *bufio.Reader
	stderr *bytes.Buffer
	ch     int
	buf    []byte
	got    int64
}

type probe struct {
	Streams []struct {
		CodecType   string `json:"codec_type"`
		CodecName   string `json:"codec_name"`
		SampleRate  string `json:"sample_rate"`
		Channels    int    `json:"channels"`
		BitsRaw     string `json:"bits_per_raw_sample"`
		BitRate     string `json:"bit_rate"`
		Layout      string `json:"channel_layout"`
		Disposition struct {
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
	} `json:"format"`
}

var reFFTags = regexp.MustCompile(`^(\[[^\]]*\]\s*)+`)

var rePCM = regexp.MustCompile(`^pcm_([suf])(\d+)(le|be|planar)?$`)

var codecNames = map[string]string{"pcm_alaw": "A-law", "pcm_mulaw": "µ-law", "opus": "Opus", "vorbis": "Vorbis", "flac": "FLAC", "aac": "AAC", "mp3": "MP3",
	"alac": "ALAC", "speex": "Speex", "wmav2": "WMA", "ac3": "AC-3", "eac3": "E-AC-3"}

func openFFmpeg(path string, kind fileKind, why error) (pcmReader, audioInfo, error) {
	info := audioInfo{lossy: true}
	what := string(kind)
	if kind == kOgg || kind == kAudioFF || kind == kMedia {
		what = "this format"
	}
	if why != nil && !errors.Is(why, errUnsupported) {
		return nil, info, why
	}
	if msg := strings.TrimPrefix(fmt.Sprint(why), errUnsupported.Error()+": "); why != nil && msg != errUnsupported.Error() {
		what = msg
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return nil, info, fmt.Errorf("reading %s needs ffmpeg and ffprobe, and ffprobe isn't installed", what)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, info, fmt.Errorf("reading %s needs ffmpeg, and it isn't installed", what)
	}
	var stderr bytes.Buffer
	// file: keeps a name with a colon from being read as a protocol
	in := path
	if abs, err := filepath.Abs(path); err == nil {
		in = abs
	}
	in = "file:" + in
	pc := exec.Command("ffprobe", "-v", "error", "-show_entries",
		"stream=codec_type,codec_name,sample_rate,channels,channel_layout,bits_per_raw_sample,bit_rate:stream_disposition=attached_pic:format=format_name",
		"-of", "json", in)
	pc.Stderr = &stderr
	js, err := pc.Output()
	var p probe
	if jerr := json.Unmarshal(js, &p); jerr != nil || len(p.Streams) == 0 {
		if err != nil {
			return nil, info, fmt.Errorf("ffprobe can't read it: %s", strings.ReplaceAll(lastLine(stderr.String(), err), in, path))
		}
		if jerr != nil {
			return nil, info, fmt.Errorf("ffprobe's answer isn't JSON: %v", jerr)
		}
	}
	if err != nil { // it found the streams but stumbled: a damaged or cut file
		info.warn = "ffprobe reports a problem: " + strings.ReplaceAll(lastLine(stderr.String(), err), in, path)
	}
	audio := -1
	video := false
	for i, s := range p.Streams {
		switch {
		case s.CodecType == "audio" && audio < 0:
			audio = i
		case s.CodecType == "video" && s.Disposition.AttachedPic == 0:
			video = true
		}
	}
	if video {
		return nil, info, errors.New(kVideo.notMedia())
	}
	if audio < 0 {
		return nil, info, errors.New("no audio stream in it")
	}
	s := p.Streams[audio]
	info.rate, _ = strconv.Atoi(s.SampleRate)
	info.ch = s.Channels
	info.layout = s.Layout
	if info.rate <= 0 || info.ch <= 0 || info.ch > maxChannels {
		return nil, info, fmt.Errorf("ffprobe gives %d channels at %d Hz", info.ch, info.rate)
	}
	codec := codecNames[s.CodecName]
	if m := rePCM.FindStringSubmatch(s.CodecName); m != nil { // pcm_s16le, pcm_u8, pcm_f32be
		codec = "PCM " + m[2] + "-bit"
		if m[1] == "f" {
			codec = "float " + m[2] + "-bit"
		} else {
			info.bits, _ = strconv.Atoi(m[2])
		}
		info.lossy = false
	}
	if s.CodecName == "flac" || s.CodecName == "alac" {
		info.lossy = false
	}
	if codec == "" {
		codec = s.CodecName
	}
	container := map[string]string{"ogg": "Ogg", "flac": "", "mp3": "", "wav": "WAV", "aiff": "AIFF", "aac": "",
		"matroska,webm": "WebM", "mov,mp4,m4a,3gp,3g2,mj2": "M4A", "caf": "CAF", "asf": "WMA"}[p.Format.FormatName]
	if p.Format.FormatName == "matroska,webm" && strings.ToLower(filepath.Ext(path)) != ".webm" && strings.ToLower(filepath.Ext(path)) != ".weba" {
		container = "Matroska"
	}
	info.format = strings.TrimSpace(container + " " + codec)
	if container == codec || container == "WMA" {
		info.format = codec
	}
	// crccheck: FLAC's frame checksums; asetpts: the joins of chained files
	// don't upset the raw output
	cmd := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-err_detect", "crccheck", "-i", in,
		"-map", fmt.Sprintf("0:%d", audio), "-af", "asetpts=N/SR/TB",
		"-f", "f32le", "-acodec", "pcm_f32le", "-")
	cmd.Stderr = &stderr
	stderr.Reset()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, info, err
	}
	if err := cmd.Start(); err != nil {
		return nil, info, err
	}
	return &ffReader{cmd: cmd, out: bufio.NewReaderSize(out, 1<<16), stderr: &stderr, ch: info.ch}, info, nil
}

func (r *ffReader) Read(p []float32) (int, error) {
	n := len(p) / r.ch * r.ch * 4
	if cap(r.buf) < n {
		r.buf = make([]byte, n)
	}
	b := r.buf[:n]
	got, err := io.ReadFull(r.out, b)
	frame := 4 * r.ch
	got = got / frame * frame
	k := 0
	for i := 0; i+4 <= got; i += 4 {
		p[k] = math.Float32frombits(binary.LittleEndian.Uint32(b[i:]))
		k++
	}
	r.got += int64(k)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		if k > 0 {
			return k, nil
		}
		return 0, io.EOF
	}
	return k, err
}

func (r *ffReader) Close() error {
	io.Copy(io.Discard, r.out)
	if err := r.cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg can't decode it: %s", lastLine(r.stderr.String(), err))
	}
	if s := strings.TrimSpace(r.stderr.String()); s != "" { // it played on past damage
		return fmt.Errorf("ffmpeg reports a problem: %s", lastLine(s, nil))
	}
	return nil
}

// lastLine is the last line a command wrote to stderr, or its error.
func lastLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return err.Error()
	}
	lines := strings.Split(s, "\n")
	// ffmpeg's "[aist#0:0/flac @ 0x55…] [dec:flac @ 0x55…] " says nothing to a reader
	return reFFTags.ReplaceAllString(strings.TrimSpace(lines[len(lines)-1]), "")
}
