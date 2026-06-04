//go:build windows

package platform

import (
	"fmt"
	"image"
	"unsafe"
)

// bitmapInfoHeader mirrors Win32 BITMAPINFOHEADER.
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

func (d *winDriver) CaptureScreen() (image.Image, error) {
	return d.CaptureBounds(virtualBounds())
}

// CaptureBounds grabs a screen-absolute rectangle via GDI BitBlt + GetDIBits.
// Note: this captures whatever is visually on screen at those coordinates, so an
// occluded window yields the occluding pixels — prefer CaptureWindow for windows.
func (d *winDriver) CaptureBounds(b Bounds) (image.Image, error) {
	if b.Width <= 0 || b.Height <= 0 {
		return nil, fmt.Errorf("capture: invalid bounds %dx%d", b.Width, b.Height)
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC(screen) failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	bmp, _, _ := procCreateCompatibleBmp.Call(screenDC, uintptr(b.Width), uintptr(b.Height))
	if bmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bmp)

	old, _, _ := procSelectObject.Call(memDC, bmp)
	defer procSelectObject.Call(memDC, old)

	res, _, _ := procBitBlt.Call(memDC, 0, 0, uintptr(b.Width), uintptr(b.Height),
		screenDC, uintptr(int32(b.X)), uintptr(int32(b.Y)), srccopy)
	if res == 0 {
		return nil, fmt.Errorf("BitBlt failed")
	}
	return dibToImage(memDC, bmp, b.Width, b.Height)
}

// CaptureWindow grabs a window's own pixels via PrintWindow, so the capture is
// correct even when the window is occluded or not focused. If PrintWindow
// returns a (near) black frame — some GPU-composited apps do — it falls back to
// a screen-region BitBlt of the window's bounds.
func (d *winDriver) CaptureWindow(w Window) (image.Image, error) {
	b, err := w.Bounds()
	if err != nil {
		return nil, err
	}
	if b.Width <= 0 || b.Height <= 0 {
		return nil, fmt.Errorf("capture: window has zero size")
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("GetDC(screen) failed")
	}
	defer procReleaseDC.Call(0, screenDC)

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, fmt.Errorf("CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(memDC)

	bmp, _, _ := procCreateCompatibleBmp.Call(screenDC, uintptr(b.Width), uintptr(b.Height))
	if bmp == 0 {
		return nil, fmt.Errorf("CreateCompatibleBitmap failed")
	}
	defer procDeleteObject.Call(bmp)

	old, _, _ := procSelectObject.Call(memDC, bmp)
	defer procSelectObject.Call(memDC, old)

	res, _, _ := procPrintWindow.Call(w.Handle(), memDC, pwRenderFullContent)
	if res != 0 {
		img, err := dibToImage(memDC, bmp, b.Width, b.Height)
		if err == nil && !isMostlyBlack(img) {
			return img, nil
		}
	}
	// Fallback: occlusion-prone screen-region capture.
	return d.CaptureBounds(b)
}

// dibToImage reads a 32-bit DIB out of a memory DC into an RGBA image.
func dibToImage(memDC, bmp uintptr, w, h int) (image.Image, error) {
	bi := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      -int32(h), // negative => top-down rows
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}}
	buf := make([]byte, w*h*4)
	res, _, _ := procGetDIBits.Call(memDC, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	if res == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(buf); i += 4 {
		img.Pix[i+0] = buf[i+2] // R
		img.Pix[i+1] = buf[i+1] // G
		img.Pix[i+2] = buf[i+0] // B
		img.Pix[i+3] = 255      // A
	}
	return img, nil
}

// isMostlyBlack reports whether a sampled image is essentially all black,
// which is how PrintWindow fails for some GPU-composited windows.
func isMostlyBlack(img image.Image) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return true
	}
	step := 8
	var sampled, black int
	for y := 0; y < h; y += step {
		for x := 0; x < w; x += step {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if (r>>8)+(g>>8)+(bl>>8) < 24 {
				black++
			}
			sampled++
		}
	}
	return sampled > 0 && float64(black)/float64(sampled) > 0.99
}
