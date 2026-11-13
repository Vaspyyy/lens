package main

import (
	"io"
	"math"
	"math/cmplx"
	"sort"
)

const (
	fftSize    = 2048
	maxBlocks  = 1 << 18 // level blocks kept before they double in length
	maxFrames  = 4096    // spectrum frames kept before the hop doubles
	picBins    = 128     // log-frequency rows of the spectrogram picture
	picLowFreq = 30.0
)

// textBands are the spectrum rows of the card, from low to high.
var textBands = []struct {
	lo, hi float64
	label  string
}{{0, 200, "under 200"}, {200, 800, "200-800"}, {800, 3000, "800-3k"}, {3000, 8000, "3k-8k"}, {8000, math.Inf(1), "8k+ Hz"}}

type specFrame struct {
	center int64 // its middle, in frames from the start
	bands  [5]float32
	bins   [picBins]float32
	loud   bool
}

// soundFacts is what lens knows about one sound.
type soundFacts struct {
	path      string
	info      audioInfo
	rate, ch  int
	frames    int64
	threshold float64 // silence, dBFS
	warning   string  // decoding stopped early: the card is of the part read

	peak, sumsq, sum []float64
	clipRuns         int
	clipSamples      int
	over             int
	run              []int

	sumLR, maxDiff float64

	blockLen int       // frames a level block covers
	blocks   []float32 // each block's peak over all channels
	cur      float32
	curN     int

	kf      []kFilter
	kw      []float64
	sub     []float64 // K-weighted energy of each 100 ms
	subAcc  float64
	subN    int
	subLen  int
	kTotal  float64
	lufs    float64 // integrated loudness; -Inf for silence
	lufsEst bool    // a clip under 400 ms: over the whole clip

	hop     int
	ring    [fftSize]float32
	pos     int
	filled  int64
	since   int
	spec    []specFrame
	avg     []float64
	avgN    int
	flats   []float64
	window  [fftSize]float64
	fftBuf  []complex128
	binLo   [picBins]int
	binHi   [picBins]int
	bandOf  []int8
	k50     int
	k12k    int
	picHigh float64
}

func newSound(path string, info audioInfo, threshold float64) *soundFacts {
	s := &soundFacts{path: path, info: info, rate: info.rate, ch: info.ch, threshold: threshold}
	s.peak = make([]float64, s.ch)
	s.sumsq = make([]float64, s.ch)
	s.sum = make([]float64, s.ch)
	s.run = make([]int, s.ch)
	s.blockLen = max(1, s.rate/1000)
	s.subLen = max(1, s.rate/10)
	s.kf = make([]kFilter, s.ch)
	s.kw = make([]float64, s.ch)
	for c := range s.kf {
		s.kf[c] = newKFilter(float64(s.rate))
		s.kw[c] = 1
		if s.ch == 6 && c == 3 {
			s.kw[c] = 0 // LFE
		}
		if s.ch == 6 && c >= 4 {
			s.kw[c] = 1.41 // surround
		}
	}
	s.hop = 256
	s.avg = make([]float64, fftSize/2+1)
	s.fftBuf = make([]complex128, fftSize)
	for i := range s.window {
		s.window[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1))
	}
	nyq := float64(s.rate) / 2
	s.picHigh = min(nyq, 20000)
	hz := float64(s.rate) / fftSize
	s.bandOf = make([]int8, fftSize/2+1)
	for k := range s.bandOf {
		f := float64(k) * hz
		for b, tb := range textBands {
			if f >= tb.lo && f < tb.hi {
				s.bandOf[k] = int8(b)
			}
		}
	}
	for j := 0; j < picBins; j++ {
		f0 := picLowFreq * math.Pow(s.picHigh/picLowFreq, float64(j)/picBins)
		f1 := picLowFreq * math.Pow(s.picHigh/picLowFreq, float64(j+1)/picBins)
		lo, hi := int(math.Ceil(f0/hz)), int(math.Floor(f1/hz))
		if hi < lo { // no FFT bin in this narrow band: the nearest one
			lo = int(math.Round((f0 + f1) / 2 / hz))
			hi = lo
		}
		s.binLo[j], s.binHi[j] = min(lo, fftSize/2), min(hi, fftSize/2)
	}
	s.k50 = max(1, int(50/hz))
	s.k12k = min(fftSize/2, int(min(12000, nyq)/hz))
	return s
}

// analyzeAudioFile decodes a sound and measures it.
func analyzeAudioFile(path string, kind fileKind, o options) (*soundFacts, error) {
	src, info, err := openAudio(path, kind)
	if err != nil {
		return nil, err
	}
	s := newSound(path, info, o.silence)
	buf := make([]float32, 4096*info.ch)
	var readErr error
	for {
		n, err := src.Read(buf)
		if n > 0 {
			s.feed(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			readErr = err
			break
		}
	}
	closeErr := src.Close()
	if readErr != nil && s.frames == 0 {
		return nil, readErr
	}
	if closeErr != nil && s.frames == 0 {
		return nil, closeErr
	}
	s.finish()
	switch { // part of it was read: shown, with what went wrong
	case readErr != nil:
		s.warning = readErr.Error()
	case closeErr != nil:
		s.warning = closeErr.Error()
	}
	return s, nil
}

// feed takes interleaved samples, a whole number of frames.
func (s *soundFacts) feed(p []float32) {
	ch := s.ch
	for i := 0; i+ch <= len(p); i += ch {
		var mono, bp float64
		for c := 0; c < ch; c++ {
			x := float64(p[i+c])
			ax := math.Abs(x)
			if ax > s.peak[c] {
				s.peak[c] = ax
			}
			s.sumsq[c] += x * x
			s.sum[c] += x
			if ax >= 0.9999 {
				s.run[c]++
			} else {
				s.endRun(c)
			}
			if ax > 1.0001 {
				s.over++
			}
			bp = max(bp, ax)
			mono += x
			if s.kw[c] != 0 {
				y := s.kf[c].process(x)
				s.subAcc += s.kw[c] * y * y
			}
		}
		if ch >= 2 {
			l, r := float64(p[i]), float64(p[i+1])
			s.sumLR += l * r
			s.maxDiff = max(s.maxDiff, math.Abs(l-r))
		}
		s.frames++
		// level blocks
		s.cur = max(s.cur, float32(bp))
		s.curN++
		if s.curN >= s.blockLen {
			s.pushBlock()
		}
		// loudness: 100 ms parts
		s.subN++
		if s.subN == s.subLen {
			s.sub = append(s.sub, s.subAcc/float64(s.subLen))
			s.kTotal += s.subAcc
			s.subAcc, s.subN = 0, 0
		}
		// spectrum
		s.ring[s.pos] = float32(mono / float64(ch))
		s.pos = (s.pos + 1) % fftSize
		s.filled++
		s.since++
		if s.filled >= fftSize && s.since >= s.hop {
			s.since = 0
			s.frame(s.filled - fftSize/2)
		}
	}
}

func (s *soundFacts) endRun(c int) {
	if s.run[c] >= 3 {
		s.clipRuns++
		s.clipSamples += s.run[c]
	}
	s.run[c] = 0
}

func (s *soundFacts) pushBlock() {
	s.blocks = append(s.blocks, s.cur)
	s.cur, s.curN = 0, 0
	if len(s.blocks) >= maxBlocks {
		half := s.blocks[:len(s.blocks)/2]
		for i := range half {
			half[i] = max(s.blocks[2*i], s.blocks[2*i+1])
		}
		s.blocks = half
		s.blockLen *= 2
	}
}

// frame analyzes the last fftSize samples (zeros before the start).
func (s *soundFacts) frame(center int64) {
	var peak float64
	for i := 0; i < fftSize; i++ {
		x := float64(s.ring[(s.pos+i)%fftSize])
		peak = max(peak, math.Abs(x))
		s.fftBuf[i] = complex(x*s.window[i], 0)
	}
	fft(s.fftBuf)
	var f specFrame
	f.center = center
	f.loud = peak > dbToLin(s.threshold)
	pw := make([]float64, fftSize/2+1)
	for k := range pw {
		a := cmplx.Abs(s.fftBuf[k])
		pw[k] = a * a
		f.bands[s.bandOf[k]] += float32(pw[k])
	}
	for j := 0; j < picBins; j++ {
		var sum float64
		for k := s.binLo[j]; k <= s.binHi[j]; k++ {
			sum += pw[k]
		}
		f.bins[j] = float32(sum / float64(s.binHi[j]-s.binLo[j]+1))
	}
	if f.loud {
		for k := range pw {
			s.avg[k] += pw[k]
		}
		s.avgN++
		// flatness per octave, weighted by the octave's energy: about 0.5
		// for noise of any color, near 0 for a tone
		var wsum, esum float64
		for lo := s.k50; lo <= s.k12k; lo *= 2 {
			hi := min(2*lo-1, s.k12k)
			var logSum, sum float64
			for k := lo; k <= hi; k++ {
				logSum += math.Log(pw[k] + 1e-30)
				sum += pw[k]
			}
			n := float64(hi - lo + 1)
			if sum > 0 && n >= 2 {
				wsum += sum * math.Exp(logSum/n) / (sum / n)
				esum += sum
			}
		}
		if esum > 0 {
			s.flats = append(s.flats, wsum/esum)
		}
	}
	s.spec = append(s.spec, f)
	if len(s.spec) >= maxFrames {
		half := s.spec[:len(s.spec)/2]
		for i := range half {
			a, b := s.spec[2*i], s.spec[2*i+1]
			var m specFrame
			m.center = (a.center + b.center) / 2
			m.loud = a.loud || b.loud
			for k := range m.bands {
				m.bands[k] = (a.bands[k] + b.bands[k]) / 2
			}
			for k := range m.bins {
				m.bins[k] = (a.bins[k] + b.bins[k]) / 2
			}
			half[i] = m
		}
		s.spec = half
		s.hop *= 2
	}
}

func (s *soundFacts) finish() {
	if s.curN > 0 {
		s.blocks = append(s.blocks, s.cur)
	}
	for c := range s.run {
		s.endRun(c)
	}
	if len(s.spec) == 0 && s.filled > 0 { // shorter than one FFT: zero-padded
		s.frame(s.filled / 2)
	}
	// integrated loudness, ITU-R BS.1770: 400 ms blocks every 100 ms, gated
	// at -70 LUFS and 10 LU under the mean of what passes
	s.lufs = math.Inf(-1)
	if len(s.sub) < 4 {
		if s.frames > 0 {
			z := (s.kTotal + s.subAcc) / float64(s.frames)
			s.lufs = -0.691 + 10*math.Log10(z)
			s.lufsEst = true
		}
		return
	}
	var zs []float64
	for j := 0; j+4 <= len(s.sub); j++ {
		zs = append(zs, (s.sub[j]+s.sub[j+1]+s.sub[j+2]+s.sub[j+3])/4)
	}
	gate := func(zs []float64, min float64) ([]float64, float64) {
		var kept []float64
		var sum float64
		for _, z := range zs {
			if -0.691+10*math.Log10(z) > min {
				kept = append(kept, z)
				sum += z
			}
		}
		if len(kept) == 0 {
			return nil, 0
		}
		return kept, sum / float64(len(kept))
	}
	kept, mean := gate(zs, -70)
	if len(kept) == 0 {
		return
	}
	rel := -0.691 + 10*math.Log10(mean) - 10
	if kept2, mean2 := gate(kept, rel); len(kept2) > 0 {
		s.lufs = -0.691 + 10*math.Log10(mean2)
	}
}

func dbToLin(db float64) float64 { return math.Pow(10, db/20) }

func linToDB(x float64) float64 {
	if x <= 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(x)
}

func (s *soundFacts) duration() float64 { return float64(s.frames) / float64(s.rate) }

func (s *soundFacts) blockDur() float64 { return float64(s.blockLen) / float64(s.rate) }

// loudSpan is the first and last block above the silence threshold, or
// -1, -1 when there's none.
func (s *soundFacts) loudSpan() (int, int) {
	t := float32(dbToLin(s.threshold))
	i0, i1 := -1, -1
	for i, b := range s.blocks {
		if b > t {
			if i0 < 0 {
				i0 = i
			}
			i1 = i
		}
	}
	return i0, i1
}

// pauses are the quiet stretches of 250 ms or more between sounds.
func (s *soundFacts) pauses() [][2]float64 {
	i0, i1 := s.loudSpan()
	if i0 < 0 {
		return nil
	}
	t := float32(dbToLin(s.threshold))
	bd := s.blockDur()
	var out [][2]float64
	start := -1
	for i := i0; i <= i1; i++ {
		quiet := s.blocks[i] <= t
		if quiet && start < 0 {
			start = i
		}
		if !quiet && start >= 0 {
			if float64(i-start)*bd >= 0.25 {
				out = append(out, [2]float64{float64(start) * bd, float64(i) * bd})
			}
			start = -1
		}
	}
	return out
}

// tone is the strongest frequency of the average spectrum of the loud
// frames, how pitched the sound is, and its energy below 250 Hz, up to
// 4 kHz and above.
func (s *soundFacts) tone() (freq, flat, low, mid, high float64, ok bool) {
	if s.avgN == 0 {
		return
	}
	hz := float64(s.rate) / fftSize
	best := -1
	for k := max(2, int(math.Ceil(30/hz))); k < len(s.avg)-1; k++ {
		if best < 0 || s.avg[k] > s.avg[best] {
			best = k
		}
	}
	if best < 2 {
		return
	}
	// a peak that stands out: well over the average level around it
	var around float64
	n := 0
	for k := max(1, best-50); k <= min(len(s.avg)-1, best+50); k++ {
		if k < best-2 || k > best+2 {
			around += s.avg[k]
			n++
		}
	}
	prominent := n > 0 && s.avg[best] > 20*around/float64(n)
	// a parabola through the peak and its neighbors, in dB
	a, b, c := math.Log(s.avg[best-1]+1e-30), math.Log(s.avg[best]+1e-30), math.Log(s.avg[best+1]+1e-30)
	d := 0.0
	if den := a - 2*b + c; den != 0 {
		d = 0.5 * (a - c) / den
	}
	if prominent {
		freq = (float64(best) + max(-0.5, min(0.5, d))) * hz
	}
	var total float64
	for k, p := range s.avg {
		f := float64(k) * hz
		total += p
		switch {
		case f < 250:
			low += p
		case f < 4000:
			mid += p
		default:
			high += p
		}
	}
	if total > 0 {
		low, mid, high = low/total, mid/total, high/total
	}
	if len(s.flats) > 0 {
		fl := append([]float64(nil), s.flats...)
		sort.Float64s(fl)
		flat = fl[len(fl)/2]
	}
	return freq, flat, low, mid, high, true
}

var noteNames = []string{"C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"}

// noteName is the nearest note: 440 Hz is A4.
func noteName(f float64) string {
	if f <= 0 {
		return ""
	}
	n := int(math.Round(12*math.Log2(f/440))) + 69
	if n < 0 {
		return ""
	}
	return noteNames[n%12] + itoa(n/12-1)
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

// --- K-weighting (ITU-R BS.1770): a high shelf, then a high pass

type biquad struct{ b0, b1, b2, a1, a2, z1, z2 float64 }

func (q *biquad) process(x float64) float64 {
	y := q.b0*x + q.z1
	q.z1 = q.b1*x - q.a1*y + q.z2
	q.z2 = q.b2*x - q.a2*y
	return y
}

type kFilter struct{ shelf, hp biquad }

func (k *kFilter) process(x float64) float64 { return k.hp.process(k.shelf.process(x)) }

// newKFilter computes the filters for any sample rate, as libebur128 does.
func newKFilter(rate float64) kFilter {
	f0, g, q := 1681.974450955533, 3.999843853973347, 0.7071752369554196
	k := math.Tan(math.Pi * f0 / rate)
	vh := math.Pow(10, g/20)
	vb := math.Pow(vh, 0.4996667741545416)
	a0 := 1 + k/q + k*k
	shelf := biquad{
		b0: (vh + vb*k/q + k*k) / a0,
		b1: 2 * (k*k - vh) / a0,
		b2: (vh - vb*k/q + k*k) / a0,
		a1: 2 * (k*k - 1) / a0,
		a2: (1 - k/q + k*k) / a0,
	}
	f0, q = 38.13547087602444, 0.5003270373238773
	k = math.Tan(math.Pi * f0 / rate)
	a0 = 1 + k/q + k*k
	hp := biquad{b0: 1, b1: -2, b2: 1, a1: 2 * (k*k - 1) / a0, a2: (1 - k/q + k*k) / a0}
	return kFilter{shelf: shelf, hp: hp}
}

// --- FFT

// fft transforms x in place; len(x) is a power of two.
func fft(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := 0; k < size/2; k++ {
				a, b := x[start+k], x[start+k+size/2]*wk
				x[start+k], x[start+k+size/2] = a+b, a-b
				wk *= w
			}
		}
	}
}
