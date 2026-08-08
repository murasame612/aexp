//go:build !darwin || !cgo

package printer

import (
	"fmt"
	"image"
)

const nativeCUPSRasterSupported = false

func encodeNativeCUPSRaster(_ *image.Gray, _ string) ([]byte, error) {
	return nil, fmt.Errorf("native CUPS raster printing requires macOS with cgo enabled")
}
