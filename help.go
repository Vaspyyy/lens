package main

import (
	"fmt"
	"strings"
)

// help colors, the same 256-color palette as the other tools' help
const (
	hTitle   = "\x1b[38;5;183;1m"
	hDim     = "\x1b[38;5;59m"
	hTagline = "\x1b[38;5;146;3m"
	hSection = "\x1b[38;5;111;1m"
	hShort   = "\x1b[38;5;151m"
	hLong    = "\x1b[38;5;117m"
	hText    = "\x1b[38;5;189m"
	hReset   = "\x1b[0m"
)

type helpFlag struct{ short, long, arg, text string }

type helpPair struct{ name, text string }

// helpBlocks says what lens shows; paragraphs are kept as written.
var helpBlocks = []string{
	`Images (PNG, JPEG, GIF, WebP, BMP, TIFF): size and orientation, the color
type as stored, real transparency (clear and partly clear pixels, a clear
border) or a checkerboard painted in, the area drawn on as WxH+X+Y (the
geometry magick -crop takes), and the main colors.`,
	`Sounds (WAV, MP3, Ogg Vorbis; Opus, FLAC, M4A through ffmpeg): length,
peak, RMS and loudness in LUFS, clipping, silence at the start and end and
pauses, stereo, the strongest tone, and a text waveform and spectrum.`,
	`diff: the changed pixels grouped into spots, and one crop for both images
(or one per spot) as ready magick commands.`,
	`Several files or a folder: one line each, images and sounds apart.`,
}

var helpAfter = []struct {
	title string
	paras []string
}{}

var helpSections = []struct {
	title string
	flags []helpFlag
}{
	{"DIFF", []helpFlag{
		{"", "--fuzz", "P", "a channel may differ by P percent (default 0; 3 with a lossy image)"},
		{"", "--pad", "N", "pixels around a suggested crop (default 16)"},
	}},
	{"SOUND", []helpFlag{
		{"", "--silence", "DB", "the level that counts as silence (default -60 dBFS)"},
	}},
	{"OUTPUT", []helpFlag{
		{"-o", "", "FILE.png", "a picture: a sound's waveform and spectrogram, or a diff"},
		{"-b", "", "N", "about how many lines to print (default 60; 0: no limit)"},
		{"-A", "", "", "plain output (it is always plain for now)"},
		{"-h", "--help", "", "show this help"},
		{"-V", "--version", "", "show the version"},
	}},
}

var helpExamples = []helpPair{
	{"lens icon.png", "size, transparency, drawn area, colors"},
	{"lens sprites/", "one line per image and sound"},
	{"lens diff before.png after.png", "what changed, and one crop for both"},
	{"lens click.ogg", "levels, silence, tone, waveform"},
	{"lens voice.mp3 -o voice.png", "and a picture to look at"},
}

var helpNotes = []helpPair{
	{"exit code", "0 shown (diff too, changed or not), 2 bad input, a file not read, or a sound cut short"},
	{"NO_COLOR", "set it to turn colors off"},
}

// helpText renders the help, colored like the other tools' when color is true.
func helpText(color bool) string {
	paint := func(code, s string) string {
		if !color || s == "" {
			return s
		}
		return code + s + hReset
	}
	const flagWidth = 24
	var b strings.Builder
	section := func(title string) {
		fmt.Fprintf(&b, "\n  %s\n", paint(hSection, title))
	}
	pairs := func(ps []helpPair, w int, nameColor, textColor string) {
		for _, p := range ps {
			w = max(w, len(p.name))
		}
		for _, p := range ps {
			pad := strings.Repeat(" ", w-len(p.name)+2)
			fmt.Fprintf(&b, "    %s%s%s\n", paint(nameColor, p.name), pad, paint(textColor, p.text))
		}
	}

	fmt.Fprintf(&b, "\n  %s %s  %s\n", paint(hTitle, "✦ lens"), paint(hDim, "v"+version),
		paint(hTagline, "facts about images and sounds that looking doesn't give"))

	section("USAGE")
	fmt.Fprintf(&b, "    lens %s %s\n    lens diff %s %s %s\n", paint(hDim, "[flags]"), paint(hLong, "FILE|FOLDER..."), paint(hDim, "[flags]"), paint(hLong, "BEFORE"), paint(hLong, "AFTER"))

	prose := func(title string, paras []string) {
		section(title)
		for i, p := range paras {
			if i > 0 {
				b.WriteString("\n")
			}
			for _, line := range strings.Split(p, "\n") {
				fmt.Fprintf(&b, "    %s\n", paint(hText, line))
			}
		}
	}
	prose("WHAT IT SHOWS", helpBlocks)

	for _, s := range helpSections {
		section(s.title)
		for _, f := range s.flags {
			left, plain := "    ", "    "
			if f.short != "" {
				left = paint(hShort, f.short)
				plain = f.short
				if f.long != "" {
					left += paint(hDim, ", ")
					plain += ", "
				}
			}
			if f.long != "" {
				left += paint(hLong, f.long)
				plain += f.long
			}
			if f.arg != "" {
				left += " " + paint(hDim, f.arg)
				plain += " " + f.arg
			}
			fmt.Fprintf(&b, "    %s%s%s\n", left, strings.Repeat(" ", max(flagWidth-len(plain), 2)), paint(hText, f.text))
		}
	}

	for _, a := range helpAfter {
		prose(a.title, a.paras)
	}
	section("EXAMPLES")
	pairs(helpExamples, flagWidth-2, hLong, hDim)
	section("GOOD TO KNOW")
	pairs(helpNotes, 0, hLong, hDim)
	b.WriteString("\n")
	return b.String()
}
