//go:build darwin && cgo

package printer

/*
#cgo LDFLAGS: -lcups

#include <cups/raster.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int aexpWriteCUPSRaster(
	int fd,
	unsigned char *pixels,
	unsigned width,
	unsigned height,
	unsigned dpi,
	const char *media,
	char *errorBuffer,
	size_t errorBufferSize
) {
	cups_raster_t *raster = cupsRasterOpen(fd, CUPS_RASTER_WRITE);
	if (!raster) {
		snprintf(errorBuffer, errorBufferSize, "cupsRasterOpen: %s", cupsRasterErrorString());
		return -1;
	}

	cups_page_header2_t header;
	memset(&header, 0, sizeof(header));
	snprintf(header.OutputType, sizeof(header.OutputType), "Normal");
	header.HWResolution[0] = dpi;
	header.HWResolution[1] = dpi;
	const float heightPoints = ((float)height * 72.0f) / (float)dpi;
	const unsigned heightPointsCeil = (unsigned)heightPoints +
		(heightPoints > (float)((unsigned)heightPoints) ? 1u : 0u);
	header.ImagingBoundingBox[0] = 0;
	header.ImagingBoundingBox[1] = 0;
	header.ImagingBoundingBox[2] = 204;
	header.ImagingBoundingBox[3] = heightPointsCeil;
	header.NumCopies = 1;
	header.PageSize[0] = 204;
	header.PageSize[1] = heightPointsCeil;
	header.cupsWidth = width;
	header.cupsHeight = height;
	header.cupsBitsPerColor = 8;
	header.cupsBitsPerPixel = 8;
	header.cupsBytesPerLine = width;
	header.cupsColorOrder = CUPS_ORDER_CHUNKED;
	header.cupsColorSpace = CUPS_CSPACE_K;
	header.cupsCompression = 0;
	header.cupsRowCount = 8;
	header.cupsNumColors = 1;
	header.cupsBorderlessScalingFactor = 1.0f;
	header.cupsPageSize[0] = 204.0f;
	header.cupsPageSize[1] = heightPoints;
	header.cupsImagingBBox[0] = 0.0f;
	header.cupsImagingBBox[1] = 0.0f;
	header.cupsImagingBBox[2] = 204.0f;
	header.cupsImagingBBox[3] = heightPoints;
	snprintf(header.cupsPageSizeName, sizeof(header.cupsPageSizeName), "%s", media);

	if (!cupsRasterWriteHeader2(raster, &header)) {
		snprintf(errorBuffer, errorBufferSize, "cupsRasterWriteHeader2: %s", cupsRasterErrorString());
		cupsRasterClose(raster);
		return -2;
	}
	const unsigned expected = width * height;
	const unsigned written = cupsRasterWritePixels(raster, pixels, expected);
	if (written != expected) {
		snprintf(
			errorBuffer,
			errorBufferSize,
			"cupsRasterWritePixels: wrote %u/%u: %s",
			written,
			expected,
			cupsRasterErrorString()
		);
		cupsRasterClose(raster);
		return -3;
	}
	cupsRasterClose(raster);
	return 0;
}
*/
import "C"

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"os"
	"unsafe"
)

const nativeCUPSRasterSupported = true

func encodeNativeCUPSRaster(bitmap *image.Gray, media string) ([]byte, error) {
	bounds := bitmap.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width != printerWidthDots || height <= 0 {
		return nil, fmt.Errorf("invalid bitmap dimensions %dx%d", width, height)
	}

	// DeviceK uses 0 for white and 255 for black. Thresholding removes
	// anti-aliased gray edges so the thermal head receives crisp binary dots.
	pixels := binaryDeviceK(bitmap)

	temp, err := os.CreateTemp("", "aexp-cups-raster-*.ras")
	if err != nil {
		return nil, fmt.Errorf("create raster buffer: %w", err)
	}
	path := temp.Name()
	defer os.Remove(path)
	defer temp.Close()

	cMedia := C.CString(media)
	defer C.free(unsafe.Pointer(cMedia))
	errorBuffer := make([]byte, 512)
	result := C.aexpWriteCUPSRaster(
		C.int(temp.Fd()),
		(*C.uchar)(unsafe.Pointer(&pixels[0])),
		C.uint(width),
		C.uint(height),
		C.uint(printerDPI),
		cMedia,
		(*C.char)(unsafe.Pointer(&errorBuffer[0])),
		C.size_t(len(errorBuffer)),
	)
	if result != 0 {
		detail := string(bytes.TrimRight(errorBuffer, "\x00"))
		return nil, fmt.Errorf("libcups raster writer failed (%d): %s", int(result), detail)
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind raster buffer: %w", err)
	}
	payload, err := io.ReadAll(temp)
	if err != nil {
		return nil, fmt.Errorf("read raster buffer: %w", err)
	}
	if len(payload) < 4 {
		return nil, fmt.Errorf("libcups produced a truncated raster payload")
	}
	validSync := bytes.Equal(payload[:4], []byte("RaS2")) ||
		bytes.Equal(payload[:4], []byte("2SaR")) ||
		bytes.Equal(payload[:4], []byte("RaS3")) ||
		bytes.Equal(payload[:4], []byte("3SaR"))
	if !validSync {
		return nil, fmt.Errorf("libcups produced an invalid raster sync word % x", payload[:4])
	}
	return payload, nil
}
