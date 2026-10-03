package core

import (
	"bytes"
	"image"
	"image/jpeg"

	"golang.org/x/image/draw"
)

// DefaultMaxImageEdge caps the screenshot's longer side; larger images cost
// tokens without helping the model read the window.
const DefaultMaxImageEdge = 1568

const jpegQuality = 80

// EncodedImage is a scaled JPEG screenshot.
type EncodedImage struct {
	Data   []byte
	Width  int
	Height int
}

// EncodeJPEG downscales img so its longer side is at most maxEdge pixels and
// encodes it as JPEG. Images already within the cap keep their size.
func EncodeJPEG(img image.Image, maxEdge int) (EncodedImage, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if long := max(w, h); long > maxEdge {
		w = max(1, w*maxEdge/long)
		h = max(1, h*maxEdge/long)
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return EncodedImage{}, err
	}
	return EncodedImage{Data: buf.Bytes(), Width: w, Height: h}, nil
}
