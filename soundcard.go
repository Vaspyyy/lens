package main

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

func rateText(r int) string {
	return strconv.FormatFloat(float64(r)/1000, 'f', -1, 64) + " kHz"
}

func channelsText(n int) string {
	switch n {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	}
	return fmt.Sprintf("%d channels", n)
}

func dbText(x float64) string {
	if x <= 0 {
		return "-inf dBFS"
	}
	return oneDecimal(linToDB(x)) + " dBFS"
}

// secText is a time for people: 12 ms, 1.25 s, 3:25.4.
func secText(s float64) string {
	switch {
	case math.Round(s*1000) < 1000:
		return fmt.Sprintf("%.0f ms", s*1000)
	case s > 1e8: // past what a time.Duration holds well: a header's nonsense
		return fmt.Sprintf("%.3g s", s)
	}
	return durText(secDur(s))
}

func freqText(f float64) string {
	if f < 1000 {
		return fmt.Sprintf("%.0f Hz", f)
	}
	return strconv.FormatFloat(math.Round(f/10)/100, 'f', -1, 64) + " kHz"
}

// soundHeader: format, length, rate and channels, file size and bitrate.
func soundHeader(s *soundFacts) string {
	parts := []string{s.info.format}
	if s.info.detail != "" {
		parts[0] += " " + s.info.detail
	}
	parts = append(parts, secText(s.duration()), rateText(s.rate)+" "+channelsText(s.ch))
	size := sizeText(s.info.size)
	switch {
	case !s.info.lossy:
	case s.duration() > 0.5:
		bytes := s.info.size
		if s.info.audio > 0 { // not the cover art in the tag
			bytes = s.info.audio
		}
		size += fmt.Sprintf(" (~%.0f kb/s)", float64(bytes)*8/s.duration()/1000)
	}
	return strings.Join(append(parts, size), ", ")
}

func (s *soundFacts) maxPeak() float64 {
	p := 0.0
	for _, v := range s.peak {
		p = max(p, v)
	}
	return p
}

func (s *soundFacts) rms() float64 {
	var sum float64
	for _, v := range s.sumsq {
		sum += v
	}
	if s.frames == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(s.frames*int64(s.ch)))
}

func lufsText(s *soundFacts) string {
	switch {
	case math.IsNaN(s.lufs):
		return "loudness not measured (BS.1770 needs a sample rate of 4 kHz or more)"
	case math.IsInf(s.lufs, -1):
		return "loudness: silent"
	case s.lufsEst:
		return fmt.Sprintf("loudness ≈%s LUFS (under 400 ms, so over the whole clip)", oneDecimal(s.lufs))
	}
	return fmt.Sprintf("loudness %s LUFS", oneDecimal(s.lufs))
}

// oneDecimal is %.1f without a "-0.0".
func oneDecimal(x float64) string {
	s := fmt.Sprintf("%.1f", x)
	if s == "-0.0" {
		return "0.0"
	}
	return s
}

func clipText(s *soundFacts) string {
	switch {
	case s.over > 0:
		return fmt.Sprintf("%s over full scale (float), clipped when converted", plural(s.over, "sample"))
	case s.clipRuns > 0:
		return fmt.Sprintf("clipped: %s at full scale in %s", plural(s.clipSamples, "sample"), plural(s.clipRuns, "run"))
	case s.maxPeak() >= 0.9999:
		return "touches full scale, no clipped runs"
	}
	return "no clipping"
}

func stereoText(s *soundFacts) string {
	if s.ch < 2 {
		return ""
	}
	first := ""
	if s.ch > 2 {
		first = " (the first two channels)"
	}
	quiet := dbToLin(-90)
	l, r := s.sumsq[0], s.sumsq[1]
	switch {
	case s.maxDiff < 1e-4:
		return "both channels the same (mono in stereo)" + first
	case s.peak[0] < quiet && s.peak[1] < quiet:
		return "both channels silent" + first
	case s.peak[1] < quiet:
		return "the right channel is silent" + first
	case s.peak[0] < quiet:
		return "the left channel is silent" + first
	}
	corr := s.sumLR / math.Sqrt(l*r)
	bal := 10 * math.Log10(l/r)
	switch {
	case corr < -0.9:
		return fmt.Sprintf("opposite phase (correlation %.2f): the channels cancel when mixed to mono", corr) + first
	case bal > 3:
		return fmt.Sprintf("left %.1f dB louder than right, correlation %.2f", bal, corr) + first
	case bal < -3:
		return fmt.Sprintf("right %.1f dB louder than left, correlation %.2f", -bal, corr) + first
	case corr > 0.95:
		return fmt.Sprintf("nearly the same (correlation %.2f)", corr) + first
	}
	return fmt.Sprintf("the channels differ (correlation %.2f)", corr) + first
}

func toneText(s *soundFacts) string {
	f, flat, low, mid, high, ok := s.tone()
	if !ok {
		return ""
	}
	kind := "mixed: some tone, some noise"
	switch {
	case flat < 0.1:
		kind = "pitched"
	case flat > 0.35:
		kind = "noise-like"
	}
	share := func(x float64) string { return pct(int(math.Round(x*1000)), 1000) }
	peak := ""
	if f > 0 {
		peak = fmt.Sprintf(", strongest at %s (%s)", freqText(f), noteName(f))
	}
	return fmt.Sprintf("%s%s; energy under 250 Hz %s, 250 Hz-4 kHz %s, above %s",
		kind, peak, share(low), share(mid), share(high))
}

var waveChars = []rune("▁▂▃▄▅▆▇█")

// waveChar is one column of the waveform: blank below the threshold, then
// eight heights in dB up to full scale.
func waveChar(p float64, threshold float64) rune {
	t := dbToLin(threshold)
	if p <= t {
		return ' '
	}
	lvl := (linToDB(p) - threshold) / -threshold
	i := int(math.Ceil(lvl*8)) - 1
	return waveChars[max(0, min(7, i))]
}

// columns splits the blocks into n columns: each column's first and last
// block (exclusive).
func (s *soundFacts) columns(n int) [][2]int {
	nb := len(s.blocks)
	n = min(n, nb)
	out := make([][2]int, n)
	for i := range out {
		out[i] = [2]int{i * nb / n, (i + 1) * nb / n}
	}
	return out
}

func (s *soundFacts) waveRow(n int) string {
	var b strings.Builder
	for _, c := range s.columns(n) {
		var p float32
		for _, v := range s.blocks[c[0]:c[1]] {
			p = max(p, v)
		}
		b.WriteRune(waveChar(float64(p), s.threshold))
	}
	return b.String()
}

// spectrumRows are the five bands over the same columns as the waveform,
// highest first, shaded by level against the loudest cell.
func (s *soundFacts) spectrumRows(n int) []string {
	cols := s.columns(n)
	if len(s.spec) == 0 || len(cols) == 0 {
		return nil
	}
	t := float32(dbToLin(s.threshold))
	cells := make([][5]float64, len(cols))
	loud := make([]bool, len(cols))
	var top float64
	j := 0
	for i, c := range cols {
		for _, v := range s.blocks[c[0]:c[1]] {
			loud[i] = loud[i] || v > t
		}
		start, end := int64(c[0])*int64(s.blockLen), int64(c[1])*int64(s.blockLen)
		var sum [5]float64
		cnt := 0
		for j < len(s.spec) && s.spec[j].center < start {
			j++
		}
		for k := j; k < len(s.spec) && s.spec[k].center < end; k++ {
			for b := range sum {
				sum[b] += float64(s.spec[k].bands[b])
			}
			cnt++
		}
		if cnt == 0 { // fewer frames than columns: the nearest frame
			mid := (start + end) / 2
			best := 0
			for k := range s.spec {
				if abs64(s.spec[k].center-mid) < abs64(s.spec[best].center-mid) {
					best = k
				}
			}
			for b := range sum {
				sum[b] = float64(s.spec[best].bands[b])
			}
			cnt = 1
		}
		for b := range sum {
			cells[i][b] = sum[b] / float64(cnt)
			if loud[i] {
				top = max(top, cells[i][b])
			}
		}
	}
	rows := make([]string, 5)
	for b := 4; b >= 0; b-- {
		var sb strings.Builder
		for i := range cols {
			r := ' '
			if loud[i] && top > 0 && cells[i][b] > 0 {
				db := 10 * math.Log10(cells[i][b]/top)
				switch {
				case db >= -6:
					r = '█'
				case db >= -15:
					r = '▓'
				case db >= -30:
					r = '▒'
				case db >= -50:
					r = '░'
				}
			}
			sb.WriteRune(r)
		}
		rows[4-b] = sb.String()
	}
	return rows
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// renderAudio writes the card of one sound.
func renderAudio(w io.Writer, s *soundFacts, o options) {
	fmt.Fprintf(w, "lens: %s  %s\n", s.path, soundHeader(s))
	line := func(label, text string) {
		if text != "" {
			fmt.Fprintf(w, "%-9s %s\n", label, text)
		}
	}
	if s.warning != "" {
		line("warning", s.warning+"; the rest is about the part that was read")
	}
	if s.frames == 0 {
		line("samples", "none: it decodes to nothing")
		return
	}
	level := fmt.Sprintf("peak %s, RMS %s, %s; %s", dbText(s.maxPeak()), dbText(s.rms()), lufsText(s), clipText(s))
	for c, v := range s.sum {
		if dc := v / float64(s.frames); math.Abs(dc) > 0.02 {
			level += fmt.Sprintf("; DC offset %.3f in channel %d", dc, c+1)
			break
		}
	}
	if s.bad > 0 {
		level += fmt.Sprintf("; %s weren't numbers (NaN or infinite), counted as 0", plural(s.bad, "sample"))
	}
	line("level", level)
	i0, i1 := s.loudSpan()
	if i0 < 0 {
		line("silence", fmt.Sprintf("all of it: no sample above %.0f dBFS", s.threshold))
		return
	}
	bd := s.blockDur()
	lead := float64(i0) * bd
	trail := max(0, s.duration()-float64(i1+1)*bd)
	sil := fmt.Sprintf("%s at the start, %s at the end (below %.0f dBFS)", secText(lead), secText(trail), s.threshold)
	if ps := s.pauses(); len(ps) > 0 {
		var spans []string
		for i, p := range ps {
			if i == 3 {
				spans = append(spans, "…")
				break
			}
			spans = append(spans, fmt.Sprintf("%.2f-%.2f s", p[0], p[1]))
		}
		word := "a pause"
		if len(ps) > 1 {
			word = plural(len(ps), "pause")
		}
		sil += fmt.Sprintf("; %s of 250 ms or more: %s", word, strings.Join(spans, ", "))
	}
	line("silence", sil)
	line("stereo", stereoText(s))
	line("tone", toneText(s))
	const width = 60
	cols := s.columns(width)
	colDur := s.duration() / float64(len(cols))
	fmt.Fprintf(w, "%-9s │%s│ 0 to %s, %s a column; blank: below %.0f dBFS\n", "waveform", s.waveRow(width), secText(s.duration()), secText(colDur), s.threshold)
	if o.budget == 0 || o.budget >= 14 {
		for i, r := range s.spectrumRows(width) {
			label := ""
			if i == 0 {
				label = "spectrum"
			}
			fmt.Fprintf(w, "%-9s │%s│ %s\n", label, r, textBands[4-i].label)
		}
	}
}

func secDur(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func freqRange(s *soundFacts) string {
	return fmt.Sprintf("%s to %s", freqText(picLowFreq), freqText(s.picHigh))
}
