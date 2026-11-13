<div align="center">

# lens

Facts about images and sounds that looking at them doesn't give.

[![Go](https://img.shields.io/github/go-mod/go-version/Vaspyyy/lens?style=flat-square&labelColor=1e1e2e&color=89b4fa&logo=go&logoColor=89b4fa)](go.mod)
[![Release](https://img.shields.io/github/v/tag/Vaspyyy/lens?style=flat-square&labelColor=1e1e2e&color=cba6f7&label=release)](https://github.com/Vaspyyy/lens/tags)
[![License](https://img.shields.io/github/license/Vaspyyy/lens?style=flat-square&labelColor=1e1e2e&color=a6e3a1)](LICENSE)

</div>

lens prints what can't be seen by looking at a picture or heard by an agent at all. For an image: its exact size, whether the background is really transparent or a checkerboard painted in, the box that is actually drawn on, and the main colors. For two images: what changed, and one crop that fits both. For a sound: its length, peak and loudness, clipping, silence at the start and end, the strongest tone, and a text waveform and spectrum.

It was written for AI coding agents. They can look at an image but not measure it, and they can't listen at all. So they wrote a throwaway PIL or ffmpeg command for each number: `Image.open(p).size`, `identify -format "%[opaque]"`, a crop and a second look, `ffmpeg -af ebur128`, `silencedetect`. lens gives those numbers in one command with a bounded answer. It is part of the same family as [glint](https://github.com/Vaspyyy/glint), [seek](https://github.com/Vaspyyy/seek), [recap](https://github.com/Vaspyyy/recap), [crux](https://github.com/Vaspyyy/crux), [splice](https://github.com/Vaspyyy/splice) and [peek](https://github.com/Vaspyyy/peek).

## Install

```sh
go install github.com/Vaspyyy/lens@latest
```

You'll need Go 1.26 or newer (older versions fetch the right toolchain on their own). lens is a single binary. It reads PNG, JPEG, GIF, WebP, BMP, TIFF, WAV, MP3 and Ogg Vorbis by itself; Opus, FLAC, M4A and other sound formats need `ffmpeg` and `ffprobe` on the `PATH`. It's developed on Linux and should work on macOS and the BSDs.

## Usage

```sh
lens [flags] FILE|FOLDER...
lens diff [flags] BEFORE AFTER
```

### An image

```
$ lens icon.png
lens: icon.png  PNG, 512x512 square, RGBA 16-bit, 9 KB
alpha     44% clear, under 0.1% partly (soft edges, glow or shadow); the border is clear: a real transparent background
drawn     384x384+64+64, 56% of the image (the rest is clear)
colors    #8f7cff 87%, #e7e3ff 12%; 34 distinct colors
next: magick icon.png -crop 384x384+64+64 +repage icon-trim.png   cut it to the drawn area
```

- **The first line:** the format, the size with its orientation and aspect ratio when it is a common one (`16:9`, `2:3`, `≈16:9` within 1%), the color type as the file stores it (a PNG's header says whether it has an alpha channel; `with tRNS transparency`, `palette of 37 colors`), interlaced or progressive, the frames and length of an animated GIF or PNG, and the file size.
- **alpha:** no alpha channel; an alpha channel with every pixel opaque; or the share of clear pixels (alpha 0) and of partly clear ones, where soft edges and halos are, and how much of the border is clear: 90% or more is a real transparent background, as around a cut-out; only the four corners clear says `rounded corners?`.
- **border:** the border's plain color when nearly all of it (90%) is one color, or a checkerboard painted into the pixels: two grays alternating in squares along the top, swapped one square down. That is fake transparency, as image generators sometimes paint it:

  ```
  border    a checkerboard painted into the pixels (#999999 and #666666 squares of 15 px): fake transparency
  ```

- **drawn:** the box around what isn't background, as `WxH+X+Y`, the geometry ImageMagick's `-crop` takes. With transparency it is the pixels with alpha 16 and up, and faint pixels under that are named on their own line when they reach further (generator noise does). With a plain or checkered border it is the pixels unlike it (within a little more for a JPEG, where a row or column needs a few such pixels). When the box is under 90% of the image, `next:` gives the command that cuts the image to it.
- **colors:** the main colors of the visible pixels (alpha 128 and up), close ones merged, each with its share and only those with at least 1%, then the number of distinct colors. Colors of the border are marked `(background)`. A single color in every pixel, with nothing clear, is called blank.

A GIF or an animated PNG is measured by its first frame. Lens reads PNG, JPEG, GIF, WebP (still ones), BMP and TIFF, up to 16384x16384 pixels (a bigger one is refused before it is decoded, since it would take gigabytes of memory); an SVG, an AVIF or a HEIC image is named, not read.

### Two images

```
$ lens diff before.png after.png
lens: diff before.png after.png  1180x760 both
changed  16,260 pixels (1.8%) in 2 spots, the largest difference 76%
  spot 1  97x97+40+600   9,409 pixels
  spot 2  221x31+900+30  6,851 pixels
crop     one per spot, since one crop around all would be 87% of the image
  spot 1:
  magick before.png -crop 129x129+24+584 +repage before-crop1.png
  magick after.png -crop 129x129+24+584 +repage after-crop1.png
  spot 2:
  magick before.png -crop 253x63+884+14 +repage before-crop2.png
  magick after.png -crop 253x63+884+14 +repage after-crop2.png
```

A pixel changed when one of its channels differs by more than `--fuzz` percent (0 by default, 3 when either image is lossy: a JPEG or a lossy WebP), compared on premultiplied colors so pixels that are clear in both are equal whatever color they hide. Changed pixels are grouped on an 8-pixel grid into spots: cells with changes within 4 cells of each other (about 32 pixels) join one spot. Spots are listed largest first with their tight boxes (up to 6, the rest counted). The crop is the box around every change plus `--pad` pixels (16), clamped to the image, printed once for both files so before and after line up. When there are two to six spots (after padded boxes that overlap are merged) and that box would be more than twice their padded boxes together and over a quarter of the image, lens suggests one crop per spot instead, in the spots' order, each under the spots it holds (`spot 1:`). Every spot is inside a suggested crop, listed or not. When both files have the same name but for the extension, the crops are named `shot-before-crop.png` and `shot-after-crop.png` so they don't overwrite each other. When over 80% of the changed pixels differ by under 10%, it says so and suggests `--fuzz 10`.

Images of different sizes are compared over their shared top-left area, and the first line says so. A budget (`-b`) cuts the list of crops too, with a count of the rest. Identical files say `byte for byte`; the same pixels in different files say `every pixel is the same`. `-o diff.png` writes a picture to look at: the crop from before, from after, and from after with the changed pixels in red, side by side (scaled down when wider than 2,400 pixels or taller than 1,600); when nothing changed, it says no picture was written.

### A sound

```
$ lens click.ogg
lens: click.ogg  Ogg Vorbis, 442 ms, 44.1 kHz stereo, 5 KB
level     peak -2.7 dBFS, RMS -11.5 dBFS, loudness -8.3 LUFS; no clipping
silence   9 ms at the start, 192 ms at the end (below -60 dBFS)
stereo    both channels the same (mono in stereo)
tone      pitched, strongest at 880 Hz (A5); energy under 250 Hz 0%, 250 Hz-4 kHz 100%, above 0%
waveform  │ ████████████████████▇▇▇▇▇▇▇▇▆▆▆▅▄                          │ 0 to 442 ms, 7 ms a column; blank: below -60 dBFS
spectrum  │                                                            │ 8k+ Hz
          │ ░░░                                                        │ 3k-8k
          │ █████████████████████▓▓▓▓▓▓▓▓▒▒▒▒                          │ 800-3k
          │ ▒▒▒░░░ ░ ░ ░  ░ ░                                          │ 200-800
          │ ░░░░                                                       │ under 200
```

- **The first line:** the format (and a WAV's sample format), the length, the sample rate and channels, and the file size with the average bitrate of a compressed file.
- **level:** the sample peak and the RMS level in dBFS, and the integrated loudness in LUFS as ITU-R BS.1770 defines it (K-weighting, 400 ms blocks every 100 ms, gated at -70 LUFS and 10 LU under the mean), the number loudness targets like -14 or -23 LUFS refer to. Channels are weighted by the speaker they belong to, as ffmpeg's `ebur128` does (the LFE left out, back and side channels at 1.41), from a WAV's channel mask or the usual layout for the count. A clip under 400 ms has no full block, so its loudness is over the whole clip and marked `≈`; below a 4 kHz sample rate it isn't measured, since the K-weighting isn't defined there. Clipping is runs of 3 or more samples at full scale (for 8-bit PCM, whose top is 127/128, at that), or float samples over it. A DC offset over 0.02 is named, and so are samples that aren't numbers (NaN or infinite), which are counted as 0.
- **silence:** how long the sound stays under `--silence` (-60 dBFS, ffmpeg's `silencedetect` default) at the start and at the end, and the pauses of 250 ms or more in between.
- **stereo:** both channels the same, one of them silent, in opposite phase (they cancel when mixed to mono), one more than 3 dB louder, or how alike they are (correlation).
- **tone:** pitched, noise-like or mixed (by the spectral flatness of each octave, weighted by its energy, the median over the frames that aren't silent), the strongest frequency with its note name when it stands well out of the average spectrum, and the share of energy below 250 Hz, up to 4 kHz and above.
- **waveform:** one row of 60 columns, each the peak of its slice in dB, from blank (below the silence level) up to full scale, so silence and fades show as they sound. **spectrum:** the same columns in five frequency bands, shaded by level against the loudest cell. The spectrum and the tone add up the power of each channel (up to 8), so channels that would cancel in a mono mix still show.

`-o voice.png` writes the waveform (heights in dB) over a spectrogram from 30 Hz to 20 kHz (or half the sample rate) on a log scale, a picture to look at. WAV (8, 16, 24 and 32-bit integer, 32 and 64-bit float, extensible and RF64 headers, up to 64 channels), MPEG-1 MP3 and Ogg Vorbis are decoded in Go; an MP3's encoder delay and padding, which its LAME tag records, are removed, so silence at the start is measured as it plays. Other formats go through `ffmpeg`: Opus, FLAC, M4A, MPEG-2 and 2.5 MP3s (which the Go decoder gets wrong), chained Ogg files, and anything the Go decoders can't open. A file named with a colon is passed to ffmpeg as a file, not a protocol.

A sound that ends before its header says, a damaged or cut Ogg file (each page's checksum is checked), or one that ffmpeg reports a problem with gets a `warning` line under the first one, and the rest of the card is of the part that was read; lens then exits with 2. A sound of any length is measured with little memory: the waveform keeps one peak per millisecond (fewer for long files), the spectrum at most about 4,000 frames.

### Several files or a folder

```
$ lens sprites
lens: 2 images, 1 sound
  image             size     alpha                  drawn            colors
  sprites/fake.png  512x512  painted checkerboard   273x273+120+120  #666565 #999898 #fe5050
  sprites/icon.png  512x512  clear background, 44%  384x384+64+64    #8f7cff #e7e3ff
  sound              length  format                      peak     LUFS  silence start/end  waveform
  sprites/click.ogg  442 ms  Ogg Vorbis 44.1 kHz stereo  -2.7 dB  -8.3  9 ms / 192 ms      │█████████▇▇▇▆▅          │
```

One line per file, images and sounds in their own tables. A folder is read one level deep, and what isn't an image or a sound in it is counted, not listed. Files that can't be read are listed at the end with the reason, sounds with a warning get it under their table, and lens exits with 2; so does a folder with nothing to show.

### Flags

| Flag | |
| --- | --- |
| `--fuzz P` | diff: a channel may differ by P percent (default 0; 3 with a lossy image) |
| `--pad N` | diff: pixels around a suggested crop (default 16) |
| `--silence DB` | the level that counts as silence (default -60 dBFS) |
| `-o FILE.png` | a picture: a sound's waveform and spectrogram, or a diff |
| `-b N` | about how many lines to print (default 60; 0: no limit) |
| `-A` | plain output (lens's output is always plain for now) |
| `-h`, `--help` | help |
| `-V`, `--version` | version |

Exit code: 0 shown (`lens diff` too, whether the images differ or not), 2 bad input, a file that couldn't be read, or a sound that ends before its header says it should or stops decoding partway (its card is still shown, of the part that was read, with a `warning` line).

## For coding agents

`skill/SKILL.md` is an agent skill that says when to use lens and how. Link the folder into your agent's skills folder, for Claude Code `ln -s "$PWD/skill" ~/.claude/skills/lens`.

The design, with the transcript numbers behind it, is in [docs/design.md](docs/design.md).

## License

GPL-3.0, see [LICENSE](LICENSE).
