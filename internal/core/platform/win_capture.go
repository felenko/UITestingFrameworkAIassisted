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

	bi := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(b.Width),
		Height:      -int32(b.Height), // negative => top-down rows
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}}

	buf := make([]byte, b.Width*b.Height*4)
	res, _, _ = procGetDIBits.Call(memDC, bmp, 0, uintptr(b.Height),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&bi)), dibRGBColors)
	if res == 0 {
		return nil, fmt.Errorf("GetDIBits failed")
	}

	// GDI gives BGRA; convert to RGBA with opaque alpha.
	img := image.NewRGBA(image.Rect(0, 0, b.Width, b.Height))
	for i := 0; i < len(buf); i += 4 {
		img.Pix[i+0] = buf[i+2] // R
		img.Pix[i+1] = buf[i+1] // G
		img.Pix[i+2] = buf[i+0] // B
		img.Pix[i+3] = 255      // A
	}
	return img, nil
}
