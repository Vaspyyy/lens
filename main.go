// lens prints facts about image and audio files that can't be seen by
// looking: an image's exact size, whether its background is really
// transparent, the area drawn on and its main colors; what changed between
// two images and one crop for both; and a sound's length, levels, loudness,
// silence at the ends and a text waveform.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

const version = "0.1.0"

const defaultBudget = 60

type options struct {
	files   []string
	diff    bool
	budget  int
	picture string  // -o: a picture to write
	fuzz    float64 // diff: percent a channel may differ; -1 by format
	pad     int     // diff: pixels around a crop
	silence float64 // dBFS below which audio counts as silent
	plain   bool
}

func main() {
	out := bufio.NewWriterSize(os.Stdout, 64<<10)
	code := run(os.Args[1:], out)
	out.Flush()
	os.Exit(code)
}

func run(args []string, out *bufio.Writer) int {
	o, code, msg := parse(args)
	if msg != "" {
		if code == 0 {
			fmt.Fprint(out, msg)
		} else {
			fmt.Fprintln(out, "lens: "+msg)
		}
		return code
	}
	if o.diff {
		return runDiff(out, o)
	}
	items, notes, code := expand(o.files)
	if len(items) == 1 && len(notes) == 0 && code == 0 && !items[0].fromFolder {
		return runOne(out, items[0], o)
	}
	if o.picture != "" {
		fmt.Fprintln(out, "lens: -o writes a picture of one sound or of a diff, so give one file")
		return 2
	}
	if c := runMany(out, items, notes, o); c > code {
		code = c
	}
	return code
}

// parse reads the command line. A message with code 0 is printed as is.
func parse(args []string) (options, int, string) {
	o := options{budget: defaultBudget, fuzz: -1, pad: 16, silence: -60}
	var pos []string
	flags := true
	if len(args) > 0 && args[0] == "diff" {
		o.diff = true
		args = args[1:]
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		value := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case !flags || a == "-" || !strings.HasPrefix(a, "-"):
			pos = append(pos, a)
		case a == "--":
			flags = false
		case a == "-h" || a == "--help":
			return o, 0, helpText(wantsColor(os.Stdout))
		case a == "-V" || a == "--version":
			return o, 0, "lens " + version + "\n"
		case a == "-A":
			o.plain = true
		case a == "-b" || a == "-o" || a == "--fuzz" || a == "--pad" || a == "--silence":
			v, ok := value()
			if !ok {
				return o, 2, a + " needs a value"
			}
			switch a {
			case "-b", "--pad":
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return o, 2, fmt.Sprintf("%s takes a number, not %q", a, v)
				}
				if a == "-b" {
					o.budget = n
				} else {
					o.pad = n
				}
			case "-o":
				if !strings.HasSuffix(strings.ToLower(v), ".png") {
					return o, 2, fmt.Sprintf("-o writes a PNG picture, so its name ends in .png, not %q", v)
				}
				o.picture = v
			case "--fuzz":
				f, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64)
				if err != nil || !(f >= 0 && f <= 100) {
					return o, 2, fmt.Sprintf("--fuzz takes a percent from 0 to 100, not %q", v)
				}
				o.fuzz = f
			case "--silence":
				f, err := strconv.ParseFloat(strings.TrimSuffix(v, "dB"), 64)
				if err != nil || !(f < 0 && f >= -150) {
					return o, 2, fmt.Sprintf("--silence takes a level in dBFS below 0, like -50, not %q", v)
				}
				o.silence = f
			}
		default:
			return o, 2, fmt.Sprintf("unknown flag %s (lens -h lists them)", a)
		}
	}
	o.files = pos
	switch {
	case o.diff && len(pos) != 2:
		return o, 2, "lens diff takes two images: lens diff BEFORE AFTER"
	case len(pos) == 0:
		return o, 2, "give an image or audio file, or a folder (lens -h shows how)"
	}
	for _, p := range pos {
		if p == "-" {
			return o, 2, "lens reads files, not standard input"
		}
	}
	return o, 0, ""
}

func wantsColor(f *os.File) bool {
	return term.IsTerminal(int(f.Fd())) && os.Getenv("NO_COLOR") == ""
}

// item is a file to show.
type item struct {
	path       string
	kind       fileKind
	fromFolder bool
}

// expand turns the arguments into files: a folder gives the images and
// sounds in it (one level), other files are counted.
func expand(args []string) ([]item, []string, int) {
	var items []item
	var notes []string
	code := 0
	for _, a := range args {
		fi, err := os.Stat(a)
		if err != nil {
			notes = append(notes, statError(a, err))
			code = 2
			continue
		}
		if !fi.IsDir() {
			items = append(items, item{path: a, kind: sniffFile(a)})
			continue
		}
		entries, err := os.ReadDir(a)
		if err != nil {
			notes = append(notes, err.Error())
			code = 2
			continue
		}
		other, folders := 0, 0
		found := 0
		for _, e := range entries {
			p := joinPath(a, e.Name())
			if e.IsDir() {
				folders++
				continue
			}
			k := sniffFile(p)
			if !k.isImage() && !k.isAudio() {
				other++
				continue
			}
			items = append(items, item{path: p, kind: k, fromFolder: true})
			found++
		}
		var skipped []string
		if other > 0 {
			skipped = append(skipped, plural(other, "other file"))
		}
		if folders > 0 {
			skipped = append(skipped, plural(folders, "folder"))
		}
		switch {
		case found == 0 && len(skipped) > 0:
			notes = append(notes, fmt.Sprintf("%s: no images or sounds (%s)", a, strings.Join(skipped, ", ")))
		case found == 0:
			notes = append(notes, a+": an empty folder")
		case len(skipped) > 0:
			notes = append(notes, fmt.Sprintf("%s: %s left out (lens reads one level, images and sounds)", a, strings.Join(skipped, ", ")))
		}
	}
	return items, notes, code
}

// statError says why a file can't be read, without Go's "stat" prefix.
func statError(path string, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return path + ": no such file"
	case errors.Is(err, fs.ErrPermission):
		return path + ": permission denied"
	}
	return err.Error()
}

// joinPath joins a folder and a name the way the user wrote the folder.
func joinPath(dir, name string) string {
	if dir == "." {
		return name
	}
	if strings.HasSuffix(dir, "/") {
		return dir + name
	}
	return dir + "/" + name
}

// runOne prints the card of one file.
func runOne(out io.Writer, it item, o options) int {
	switch {
	case it.kind.isImage():
		f, err := loadImage(it.path, it.kind)
		if err != nil {
			fmt.Fprintf(out, "lens: %s: %v\n", it.path, err)
			return 2
		}
		if o.picture != "" {
			fmt.Fprintln(out, "lens: -o writes a picture of a sound or of a diff; for one image there's nothing to draw")
			return 2
		}
		analyzeImage(f)
		renderImage(out, f, o)
		return 0
	case it.kind.isAudio():
		a, err := analyzeAudioFile(it.path, it.kind, o)
		if err != nil {
			fmt.Fprintf(out, "lens: %s: %v\n", it.path, err)
			return 2
		}
		renderAudio(out, a, o)
		if o.picture != "" {
			if err := writeAudioPicture(o.picture, a); err != nil {
				fmt.Fprintf(out, "lens: %v\n", err)
				return 2
			}
			fmt.Fprintf(out, "picture   %s: the waveform (heights in dB) over a spectrogram (%s, bottom to top, on a log scale)\n", o.picture, freqRange(a))
		}
		if a.warning != "" {
			return 2
		}
		return 0
	}
	fmt.Fprintf(out, "lens: %s: %s\n", it.path, it.kind.notMedia())
	return 2
}
