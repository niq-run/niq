package workspace

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// readImageLongEdge caps the long edge of an image sent to the model. Very
// large images waste tokens / exceed model limits; the image is scaled down
// (keeping aspect ratio) before it is base64-encoded.
const readImageLongEdge = 1568

// readImageQuality is the JPEG quality used when an image must be re-encoded
// to JPEG (webp/gif sources, or any alpha-stripped image).
const readImageQuality = 85

// readImagePayload decodes an image from raw bytes, normalizes it (resize,
// strip alpha, re-encode) and returns the neutral multi-modal payload shape the
// reason worker decodes into a llm.ContentImage block:
//
//	{"content": [{"type": "image", "mime": "image/jpeg", "data": "<bare base64>"}]}
//
// The data is a bare base64 string with no data: URI prefix.
func readImagePayload(raw []byte) (any, error) {
	img, srcMIME, err := decodeImage(raw)
	if err != nil {
		return nil, err
	}
	img = stripAlpha(img)
	img = resizeLongEdge(img, readImageLongEdge)

	encoded, outMIME, err := encodeImage(img, srcMIME)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"content": []any{
			map[string]any{
				"type": "image",
				"mime": outMIME,
				"data": base64.StdEncoding.EncodeToString(encoded),
			},
		},
	}, nil
}

// decodeImage decodes raw bytes using stdlib decoders. The MIME type is
// sniffed from the header so the decode path and re-encode output are grounded
// in the actual bytes, not the file suffix.
func decodeImage(raw []byte) (image.Image, string, error) {
	switch sniffImageMIME(raw) {
	case "image/png":
		img, err := png.Decode(bytes.NewReader(raw))
		return img, "image/png", err
	case "image/jpeg":
		img, err := jpeg.Decode(bytes.NewReader(raw))
		return img, "image/jpeg", err
	case "image/gif":
		g, err := gif.Decode(bytes.NewReader(raw))
		return g, "image/gif", err
	}
	return nil, "", errors.New("unsupported image format (supported: png, jpeg, gif)")
}

// sniffImageMIME infers the MIME type from the file header bytes. It is the
// arbiter of how raw bytes are interpreted; the file suffix is not trusted.
func sniffImageMIME(raw []byte) string {
	switch {
	case len(raw) >= 8 && bytes.HasPrefix(raw, []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(raw) >= 3 && bytes.HasPrefix(raw, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case len(raw) >= 6 && (bytes.HasPrefix(raw, []byte("GIF87a")) || bytes.HasPrefix(raw, []byte("GIF89a"))):
		return "image/gif"
	}
	return ""
}

// stripAlpha composites the image onto a white background, dropping any alpha
// channel. This shrinks the payload and avoids artifacts when the image is
// re-encoded as JPEG (which cannot carry alpha anyway).
func stripAlpha(img image.Image) image.Image {
	bounds := img.Bounds()
	out := image.NewRGBA(bounds)
	draw.Draw(out, bounds, image.NewUniform(image.White), image.Point{}, draw.Src)
	draw.Draw(out, bounds, img, bounds.Min, draw.Over)
	return out
}

// resizeLongEdge scales the image down (preserving aspect ratio) so its longer
// edge is at most maxEdge; images already within the bound are returned as-is.
func resizeLongEdge(img image.Image, maxEdge int) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	scale := 1.0
	if w >= h && w > maxEdge {
		scale = float64(maxEdge) / float64(w)
	} else if h > w && h > maxEdge {
		scale = float64(maxEdge) / float64(h)
	}
	if scale >= 1 {
		return img
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	scaleBilinear(dst, img)
	return dst
}

// scaleBilinear resizes src into dst using bilinear interpolation. It is a
// manual implementation of image scaling since the stdlib image/draw exposes
// only Draw/DrawMask, not a Scaler.
func scaleBilinear(dst *image.RGBA, src image.Image) {
	dstB := dst.Bounds()
	srcB := src.Bounds()
	sw, sh := srcB.Dx(), srcB.Dy()
	dw, dh := dstB.Dx(), dstB.Dy()
	if sw == 0 || sh == 0 || dw == 0 || dh == 0 {
		return
	}

	bilinear := func(fx, fy float64) (r, g, b, a uint32) {
		x := fx*float64(sw) - 0.5
		y := fy*float64(sh) - 0.5
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		xi := int(x)
		yi := int(y)
		xf := x - float64(xi)
		yf := y - float64(yi)
		if xi >= sw-1 {
			xi = sw - 2
		}
		if yi >= sh-1 {
			yi = sh - 2
		}
		p00 := src.At(srcB.Min.X+xi, srcB.Min.Y+yi)
		p10 := src.At(srcB.Min.X+xi+1, srcB.Min.Y+yi)
		p01 := src.At(srcB.Min.X+xi, srcB.Min.Y+yi+1)
		p11 := src.At(srcB.Min.X+xi+1, srcB.Min.Y+yi+1)
		r0, g0, b0, a0 := p00.RGBA()
		r1, g1, b1, a1 := p10.RGBA()
		r2, g2, b2, a2 := p01.RGBA()
		r3, g3, b3, a3 := p11.RGBA()
		w0 := (1 - xf) * (1 - yf)
		w1 := xf * (1 - yf)
		w2 := (1 - xf) * yf
		w3 := xf * yf
		r = uint32(w0*float64(r0) + w1*float64(r1) + w2*float64(r2) + w3*float64(r3))
		g = uint32(w0*float64(g0) + w1*float64(g1) + w2*float64(g2) + w3*float64(g3))
		b = uint32(w0*float64(b0) + w1*float64(b1) + w2*float64(b2) + w3*float64(b3))
		a = uint32(w0*float64(a0) + w1*float64(a1) + w2*float64(a2) + w3*float64(a3))
		return
	}

	for dy := 0; dy < dh; dy++ {
		fy := (float64(dy) + 0.5) / float64(dh)
		for dx := 0; dx < dw; dx++ {
			fx := (float64(dx) + 0.5) / float64(dw)
			r, g, b, a := bilinear(fx, fy)
			dst.SetRGBA(dstB.Min.X+dx, dstB.Min.Y+dy, color.RGBA{
				R: uint8(r >> 8),
				G: uint8(g >> 8),
				B: uint8(b >> 8),
				A: uint8(a >> 8),
			})
		}
	}
}

// encodeImage re-encodes the (already normalized) image. PNG sources stay PNG
// (lossless); JPEG and GIF sources are transcribed to JPEG since it is compact
// and GIF animation is dropped once alpha is stripped and edges resized.
func encodeImage(img image.Image, srcMIME string) ([]byte, string, error) {
	var buf bytes.Buffer
	if srcMIME == "image/png" {
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: readImageQuality}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}
