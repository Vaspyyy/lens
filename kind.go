package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// fileKind is what a file is, by its first bytes (and its name where the
// bytes don't say).
type fileKind string

const (
	kPNG     fileKind = "PNG"
	kJPEG    fileKind = "JPEG"
	kGIF     fileKind = "GIF"
	kWebP    fileKind = "WebP"
	kBMP     fileKind = "BMP"
	kTIFF    fileKind = "TIFF"
	kWAV     fileKind = "WAV"
	kMP3     fileKind = "MP3"
	kVorbis  fileKind = "Ogg Vorbis"
	kOgg     fileKind = "Ogg" // Opus, FLAC or Speex in Ogg: ffmpeg
	kFLAC    fileKind = "FLAC"
	kM4A     fileKind = "M4A"
	kAudioFF fileKind = "audio" // another audio format: ffmpeg
	kMedia   fileKind = "media" // MP4, Matroska, WebM: audio if it has no video
	kSVG     fileKind = "SVG"
	kVideo   fileKind = "video"
	kEmpty   fileKind = "empty"
	kOther   fileKind = "other"
)

func (k fileKind) isImage() bool {
	switch k {
	case kPNG, kJPEG, kGIF, kWebP, kBMP, kTIFF:
		return true
	}
	return false
}

func (k fileKind) isAudio() bool {
	switch k {
	case kWAV, kMP3, kVorbis, kOgg, kFLAC, kM4A, kAudioFF, kMedia:
		return true
	}
	return false
}

// notMedia says why a file isn't shown.
func (k fileKind) notMedia() string {
	switch k {
	case kSVG:
		return "an SVG is a vector image; lens reads raster images (PNG, JPEG, GIF, WebP, BMP, TIFF)"
	case kVideo:
		return "a video; lens reads images and sounds (ffprobe shows a video's facts)"
	case kEmpty:
		return "an empty file"
	}
	return "not an image or a sound lens knows (images: PNG, JPEG, GIF, WebP, BMP, TIFF; sounds: WAV, MP3, Ogg, Opus, FLAC, M4A)"
}

var audioExt = map[string]bool{".wav": true, ".mp3": true, ".ogg": true, ".oga": true, ".opus": true, ".flac": true,
	".m4a": true, ".aac": true, ".aif": true, ".aiff": true, ".wma": true, ".mka": true, ".weba": true, ".caf": true}

var videoExt = map[string]bool{".mp4": true, ".mkv": true, ".webm": true, ".mov": true, ".avi": true, ".m4v": true}

// sniffFile reads the start of a file and names its kind.
func sniffFile(path string) fileKind {
	f, err := os.Open(path)
	if err != nil {
		return kOther
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	return sniff(head[:n], path)
}

func sniff(b []byte, path string) fileKind {
	ext := strings.ToLower(filepath.Ext(path))
	at := func(off int, s string) bool { return len(b) >= off+len(s) && string(b[off:off+len(s)]) == s }
	switch {
	case len(b) == 0:
		return kEmpty
	case at(0, "\x89PNG\r\n\x1a\n"):
		return kPNG
	case at(0, "\xff\xd8\xff"):
		return kJPEG
	case at(0, "GIF87a"), at(0, "GIF89a"):
		return kGIF
	case at(0, "RIFF") && at(8, "WEBP"):
		return kWebP
	case at(0, "RIFF") && at(8, "WAVE"), at(0, "RF64") && at(8, "WAVE"):
		return kWAV
	case at(0, "RIFF") && at(8, "AVI "):
		return kVideo
	case at(0, "BM") && len(b) >= 26 && ext == ".bmp", at(0, "BM") && len(b) >= 18 && bmpHeader(b):
		return kBMP
	case at(0, "II*\x00"), at(0, "MM\x00*"):
		return kTIFF
	case at(0, "OggS"):
		if bytes.Contains(b, []byte("\x01vorbis")) {
			return kVorbis
		}
		return kOgg
	case at(0, "fLaC"):
		return kFLAC
	case at(0, "ID3"):
		return kMP3
	case at(4, "ftyp"):
		if at(8, "M4A ") || at(8, "M4B ") || ext == ".m4a" {
			return kM4A
		}
		return kMedia
	case at(0, "\x1a\x45\xdf\xa3"):
		return kMedia
	case at(0, "FORM") && (at(8, "AIFF") || at(8, "AIFC")):
		return kAudioFF
	case at(0, "caff"):
		return kAudioFF
	case len(b) >= 2 && b[0] == 0xff && b[1]&0xe0 == 0xe0 && (ext == ".mp3" || mp3Frame(b)):
		return kMP3
	case len(b) >= 2 && b[0] == 0xff && b[1]&0xf6 == 0xf0: // ADTS AAC
		return kAudioFF
	}
	if ext == ".svg" || bytes.Contains(b, []byte("<svg")) {
		return kSVG
	}
	if videoExt[ext] {
		return kMedia
	}
	if audioExt[ext] {
		return kAudioFF
	}
	return kOther
}

// bmpHeader: "BM" then a header size BMP knows, so text starting with BM
// isn't taken for one.
func bmpHeader(b []byte) bool {
	switch binary.LittleEndian.Uint32(b[14:18]) {
	case 12, 40, 52, 56, 64, 108, 124:
		return true
	}
	return false
}

// mp3Frame: a frame header that makes sense (layer III, a bitrate, a
// sample rate), for MP3 files without an ID3 tag or a .mp3 name.
func mp3Frame(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	layer := (b[1] >> 1) & 3
	bitrate := b[2] >> 4
	rate := (b[2] >> 2) & 3
	return layer == 1 && bitrate != 0 && bitrate != 15 && rate != 3
}
