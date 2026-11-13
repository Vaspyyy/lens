---
name: lens
description: Get facts about an image or a sound with `lens FILE` instead of PIL one-liners (`Image.open(p).size`, `.mode`, `getbbox`), `identify -format`, ffprobe, or ffmpeg `ebur128`/`silencedetect`/`volumedetect`. For an image it prints the exact size and orientation, whether the background is really transparent (clear and partly clear pixels, clear corners) or a checkerboard painted in, the box actually drawn on as `WxH+X+Y` with the `magick -crop` command, and the main colors. `lens diff before.png after.png` lists what changed in spots and prints one crop for both images (or one per spot) as ready `magick` commands. For a sound (WAV, MP3, Ogg, Opus, FLAC) it prints length, peak, RMS, loudness in LUFS, clipping, silence at the start and end, pauses, stereo, the strongest tone and a text waveform and spectrum. A folder gives one line per file. Use it to check generated art, screenshots, sprites and audio clips before using them, and to crop before/after screenshots identically.
---

# lens for agents

You can see an image but not measure it, and you can't hear a sound. `lens FILE` gives the numbers in one bounded card. Use it before trusting generated art or audio, and for before/after screenshots.

## An image

```
$ lens icon.png
lens: icon.png  PNG, 512x512 square, RGBA 16-bit, 9 KB
alpha     44% clear, under 0.1% partly (soft edges, glow or shadow); corners clear: a real transparent background
drawn     384x384+64+64, 56% of the image (the rest is clear)
colors    #8f7cff 87%, #e7e3ff 12%; 34 distinct colors
next: magick icon.png -crop 384x384+64+64 +repage icon-trim.png   cut it to the drawn area
```

| You want to know | Look at |
| --- | --- |
| Size, landscape or portrait, aspect ratio | the first line (`1536x1024 landscape 3:2`) |
| Is the background really transparent? | `alpha`: `corners clear: a real transparent background` |
| Did the generator paint a checkerboard instead? | `border    a checkerboard painted into the pixels ...: fake transparency` |
| Halos or soft glow around a cut-out | the `partly` share in `alpha` |
| Where the content is, to trim or center it | `drawn` (`WxH+X+Y`, ready for `magick -crop`) |
| Is the screenshot blank or black? | `colors    one: every visible pixel is #000000 (blank)` |

`no alpha channel` means the file can't be transparent at all; `an alpha channel, but every pixel is opaque` means it could be but isn't.

## Before and after

```
$ lens diff before.png after.png
changed  16,260 pixels (1.8%) in 2 spots, the largest difference 76%
  spot 1  97x97+40+600   9,409 pixels
  spot 2  221x31+900+30  6,851 pixels
crop     one per spot, since one crop around all would be 87% of the image
  magick before.png -crop 253x63+884+14 +repage before-crop1.png
  magick after.png -crop 253x63+884+14 +repage after-crop1.png
  ...
```

Run the printed `magick` commands as they are: both files get the same geometry, so the crops line up. Several far-apart spots get one crop each (one before/after row per spot). Add `--pad N` for more context around the change (default 16), `--fuzz 5` to ignore small differences (animated water, JPEG noise), `-o diff.png` for a picture of before, after and the changes in red to look at.

## A sound

```
$ lens click.ogg
lens: click.ogg  Ogg Vorbis, 442 ms, 44.1 kHz stereo, 5 KB
level     peak -2.7 dBFS, RMS -11.5 dBFS, loudness -8.3 LUFS; no clipping
silence   9 ms at the start, 192 ms at the end (below -60 dBFS)
stereo    both channels the same (mono in stereo)
tone      pitched, strongest at 880 Hz (A5); energy under 250 Hz 0%, 250 Hz-4 kHz 100%, above 0%
waveform  │ ████████████████████▇▇▇▇▇▇▇▇▆▆▆▅▄                          │ 0 to 442 ms, 7 ms a column; blank: below -60 dBFS
spectrum  │ ...five bands, highest first...
```

| You want to know | Look at |
| --- | --- |
| Too loud, too quiet, clipped? | `level` (peak, LUFS, `clipped: ...`) |
| Dead air to trim at the start or end | `silence` |
| Does the shape match (a click, a swell, a fade)? | `waveform` (heights in dB, blank is silence) |
| A beep's pitch, or noise vs tone | `tone` |
| Bright or bassy, a sweep | `spectrum` and the energy shares in `tone` |

Compare takes with `lens a.mp3 b.mp3` (one line each). `-o clip.png` writes the waveform over a spectrogram to look at. Still let the user judge by ear: lens measures, it doesn't listen.

## Other

- A folder: `lens assets/` gives one line per image and sound (one level deep).
- Opus, FLAC and M4A need ffmpeg installed; WAV, MP3, Ogg Vorbis and all images don't.
- Video files are named, not read (use ffprobe).
- Exit code 0 shown (diff too, changed or not); 2 bad input or a file not read.
