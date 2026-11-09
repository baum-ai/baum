package assemble

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/baum-ai/baum/bundle"
)

// MaxSessionImageEdge bounds the longer side of a stored session image. Review
// models scale larger images down before reading them, and retina UI
// screenshots resized to this size keep readable text in 20–40% fewer PNG
// bytes; at 2048 px resampling often made them larger.
const MaxSessionImageEdge = 1568

// maxDownscalePixels bounds the images downscale decodes: decoding holds every
// pixel in memory (4–8 bytes each), so a larger image is stored as captured.
const maxDownscalePixels = 24_000_000

// downscale fits a session image within MaxSessionImageEdge. A JPEG stays a
// JPEG; PNG and WebP become PNG, since the standard library encodes no WebP.
// It reports false, keeping the original, when the image already fits, when
// resizing could lose what the image shows (GIF and APNG animation, a JPEG's
// EXIF orientation), when it is too large to decode, when decoding fails, or
// when the resized encoding is not smaller than the original bytes.
func downscale(content []byte, mime string, width, height int) ([]byte, string, int, int, bool) {
	if max(width, height) <= MaxSessionImageEdge || width > maxDownscalePixels/height ||
		mime == "image/gif" || (mime == "image/png" && animatedPNG(content)) || (mime == "image/jpeg" && jpegHasEXIF(content)) {
		return nil, "", 0, 0, false
	}
	source, _, err := image.Decode(bytes.NewReader(content))
	if err != nil {
		return nil, "", 0, 0, false
	}
	w, h := MaxSessionImageEdge, max(1, height*MaxSessionImageEdge/width)
	if height > width {
		w, h = max(1, width*MaxSessionImageEdge/height), MaxSessionImageEdge
	}
	resized := shrink(source, w, h)
	var out bytes.Buffer
	if mime == "image/jpeg" {
		err = jpeg.Encode(&out, resized, &jpeg.Options{Quality: 90})
	} else {
		mime = "image/png"
		err = png.Encode(&out, resized)
	}
	if err != nil || out.Len() >= len(content) {
		return nil, "", 0, 0, false
	}
	// The re-encoded bytes go through the same checks as any captured image.
	if sniffed, sw, sh, ok := bundle.SniffImage(out.Bytes()); !ok || sniffed != mime || sw != w || sh != h {
		return nil, "", 0, 0, false
	}
	return out.Bytes(), mime, w, h, true
}

// shrink scales src to w×h with the approximate bilinear filter, which keeps
// edges crisp and adds the fewest new colours, so PNG compresses the result
// best. That filter averages a 2×2 neighbourhood, so a larger reduction first
// halves the image until it is within 2× of the target; it never needs the
// full kernel's per-row scratch buffer.
func shrink(src image.Image, w, h int) *image.NRGBA {
	for b := src.Bounds(); b.Dx() > 2*w || b.Dy() > 2*h; b = src.Bounds() {
		half := image.NewNRGBA(image.Rect(0, 0, max(w, (b.Dx()+1)/2), max(h, (b.Dy()+1)/2)))
		draw.ApproxBiLinear.Scale(half, half.Bounds(), src, b, draw.Src, nil)
		src = half
	}
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.ApproxBiLinear.Scale(out, out.Bounds(), src, src.Bounds(), draw.Src, nil)
	return out
}

// animatedPNG reports an APNG, whose acTL chunk precedes the first IDAT; the
// standard decoder would keep only its default image.
func animatedPNG(b []byte) bool {
	for i := 8; i+8 <= len(b); {
		size, kind := binary.BigEndian.Uint32(b[i:]), string(b[i+4:i+8])
		switch kind {
		case "acTL":
			return true
		case "IDAT", "IEND":
			return false
		}
		next := i + 12 + int(size)
		if size > uint32(len(b)) || next <= i {
			return false
		}
		i = next
	}
	return false
}

// jpegHasEXIF reports an APP1 Exif segment before the image data. Its
// orientation tag would be lost on re-encoding, turning a rotated photo.
func jpegHasEXIF(b []byte) bool {
	for i := 2; i+4 <= len(b) && b[i] == 0xff; {
		marker := b[i+1]
		if marker == 0xda || marker == 0xd9 { // start of scan, end of image
			return false
		}
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xe1 && size >= 8 && i+10 <= len(b) && string(b[i+4:i+10]) == "Exif\x00\x00" {
			return true
		}
		if size < 2 {
			return false
		}
		i += 2 + size
	}
	return false
}
