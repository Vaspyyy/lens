# lens: design

lens prints facts about image and audio files that an agent can't get by
looking: exact sizes, whether a background is really transparent, the area
actually drawn on, the main colors, what changed between two screenshots and
one crop for both, and for audio the length, levels, loudness, silence at the
ends and a text waveform.

## Evidence

From Claude Code transcripts on one machine (2026-11-13, all projects):

- 3,022 shell commands worked on images or audio (PIL, numpy, ImageMagick,
  ffmpeg, ffprobe, sox, `wave`).
- What they did: read image files (2,258), combined images into sheets
  (1,194: `-append`, `montage`, `paste`, ffmpeg `tile=`), resized (1,175),
  cropped (891, of which 398 `magick ... -crop WxH+X+Y +repage`), read size
  or mode (622; PIL `Image.open(p).size` alone 530), colors and pixels (350),
  alpha (184; `identify -format "%f %[channels] %[opaque]"` 31), audio probes
  (199: ffprobe durations), audio levels and silence (148: `ebur128` and
  `loudnorm` 50, `silencedetect` 14, `volumedetect` 13), bbox and trim (52),
  image diffs (49), waveform and spectrogram pictures (41).
- File kinds named in those commands: png 2,882, jpg 379, ogg 257, mp3 117,
  wav 111, mp4 84, opus 74, svg 48, webp 43, gif 17, bmp 9, tif 6, flac 5.
- Image prompts ask for "a REAL transparent background (alpha channel, no
  checkerboard)", and agents then check the alpha by hand ("about 63%
  transparent ... a pink glow around each figure that might be part of the
  alpha itself as soft halos").
- The user's own instructions ask agents to crop before/after screenshots
  with "the exact same crop", one row per changed spot, and to check audio
  "by waveform, spectrogram and level", since an agent can't hear.

The common thread: an agent can see a picture, but not its exact numbers. It
writes a throwaway PIL or ffmpeg command for each number, often several in a
row (open, print size, crop, print again). lens answers those in one call.

## Commands

```
lens [flags] FILE|FOLDER...     a card per file, a table for several
lens diff [flags] BEFORE AFTER  what changed, and one crop for both
```

### One image

```
lens: icon.png  PNG, 1024x1024 square, RGBA 8-bit, 412 KB
alpha    38% clear, 4.1% partly (soft edges); the border is clear: a real transparent background
drawn    880x880+72+72, 74% of the image (the rest is clear)
colors   #1b1530 61%, #2a2350 18%, #8f7cff 7%, #e8e4ff 3%; 2,914 distinct
next: magick icon.png -crop 880x880+72+72 +repage icon-trim.png   cut to the drawn area
```

- **Header:** format, size, orientation (landscape, portrait, square) with
  the aspect ratio when it is a common one (16:9, 3:2, ...), color type and
  bit depth as stored in the file (PNG's IHDR says whether there is an alpha
  channel; Go's decoder alone can't tell RGB from opaque RGBA), file size.
  GIF and APNG frames and duration; JPEG baseline or progressive.
- **alpha:** no alpha channel; an alpha channel with every pixel opaque; or
  the share of clear (alpha 0) and partly transparent pixels, and whether
  the border is clear (90% of it: a real transparent background; only the
  corners: rounded corners). Partly transparent pixels are where halos and soft
  edges live, so their share is always given.
- **background:** a plain border color, or a checkerboard painted into the
  pixels (two grays alternating in squares), which is fake transparency.
- **drawn:** the bounding box of what isn't background, as `WxH+X+Y`, the
  geometry ImageMagick's `-crop` takes. With transparency that is alpha
  above 0; faint pixels (alpha under 16) that reach much further are named
  separately, since generators leave such noise. Without transparency it is
  the pixels unlike the border color (within a small tolerance for JPEG).
- **colors:** the main colors of the visible pixels with their share,
  merged when close, and the number of distinct colors. One color in every
  pixel, nothing clear, is called blank; a fully clear image empty.

### Several files or a folder

One line per file, images and audio in their own tables, so `lens sprites/`
answers "which of these have a real transparent background, and how big is
each". A folder is read one level deep; other files are counted, not listed.

### diff

```
lens: diff before.png after.png  1180x760 both
changed  18,402 pixels (2.1%) in 2 spots, the largest difference 74%
  spot 1  220x40+880+20    12,010 px
  spot 2  96x96+40+600      6,392 px
crop     one per spot, since one crop for both would cover 89% of the image
  magick before.png -crop 252x72+864+4 +repage before-1.png
  magick after.png -crop 252x72+864+4 +repage after-1.png
  ...
```

- A pixel changed when a channel differs by more than the fuzz (`--fuzz P`,
  percent; 0 for lossless pairs, 3 when either is lossy). Fully clear
  pixels are equal whatever their color.
- Spots: changed pixels grouped on an 8-pixel grid, with spots closer than
  about 32 pixels (4 cells) merged, each with its tight box; largest first,
  up to 6 listed, and every spot inside a suggested crop.
- The crop: the box around all changes plus `--pad` pixels (16), clamped to
  the image. When there are several spots and one box would mostly show
  unchanged pixels, one crop per spot. The same geometry is printed for
  both files, as ready `magick` commands.
- Different sizes: said, and the shared top-left area is compared.
- `-o FILE.png` writes a picture: the crop of before, of after, and of after
  with the changes marked, side by side, to look at.

### One audio file

```
lens: click.ogg  Ogg Vorbis, 0.42 s, 44.1 kHz stereo, 9 KB
level     peak -0.3 dBFS, RMS -14.2 dBFS, loudness -12.8 LUFS; no clipping
silence   12 ms at the start, 180 ms at the end (below -60 dBFS)
stereo    both channels the same (mono in stereo)
tone      strongest at 880 Hz (A5), pitched; low 2%, mid 81%, high 17%
waveform  ▆█▇▆▅▄▃▃▂▂▂▁▁▁▁                 0 to 0.42 s, 7 ms a column
spectrum  8k+ Hz   ░░░
          2k-8k    ▒▒▒░░
          ...
```

- **level:** sample peak and RMS in dBFS, integrated loudness in LUFS (ITU-R
  BS.1770: K-weighting, 400 ms blocks, absolute and relative gates), and
  clipping: runs of 3 or more samples at full scale, or float samples over
  it.
- **silence:** below `--silence DB` (-60, ffmpeg's `silencedetect` default)
  at the start and the end, and pauses of 250 ms or more in between.
- **stereo:** identical channels, one silent channel, opposite phase, or a
  balance off by more than 3 dB.
- **tone:** the strongest frequency of the average spectrum with its note
  name, pitched or noise-like by spectral flatness, and the share of energy
  below 250 Hz, up to 4 kHz and above.
- **waveform:** one row of block characters, each column the peak of its
  slice in dB from the silence threshold to 0 dBFS, so silence shows as
  blanks and loudness as height. **spectrum:** the same columns for five
  frequency bands, shaded by level.
- `-o FILE.png` writes the waveform over a spectrogram, as a picture.

## Decoding

Pure Go, so lens stays one binary that installs with `go install`:

- Images: PNG, JPEG, GIF from the standard library; WebP, BMP and TIFF from
  `golang.org/x/image`.
- Audio: WAV (PCM 8 to 32 bit, float 32 and 64, extensible) read by lens;
  MP3 with `github.com/hajimehoshi/go-mp3` (it always gives two channels, so
  lens reads the first frame header for the real channel count); Ogg Vorbis
  with `github.com/jfreymuth/oggvorbis`.
- Opus, FLAC, M4A/AAC and anything else go through `ffprobe` and `ffmpeg`
  when they are installed (they are where these files come from anyway);
  without them lens says what is missing.

Audio is analyzed as a stream with bounded memory: level blocks start at
1 ms and double when there are too many, spectrum frames (2048-sample FFT)
start with a hop of 256 samples and double the same way, and loudness keeps
one number per 100 ms. A clip of a second is seen in detail, an hour long
file in about 4,000 slices.

## Output rules

The family's: plain text, a budget (`-b N`, about 60 lines), counts for what
is cut, `next:` lines with a command worth running. A sound cut short or
damaged is shown with a `warning` line and exits 2. Exit code 0 shown, 2 bad
input (a file that isn't an image or audio, or can't be decoded). `diff`
exits 0 whether the images differ or not, since the card says which.

## Later

- Video facts through ffprobe (size, frame rate, length, audio track) and a
  frame sheet.
- Pixel art: the scale an image was upscaled by, and its native size.
- JPEG EXIF orientation, SVG's declared size.
- `lens diff` for audio (level and length changes between two takes).
