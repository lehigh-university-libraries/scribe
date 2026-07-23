// Package uploadlimits owns the byte envelope shared by the public upload
// contract and internal image-processing services.
package uploadlimits

import "fmt"

const (
	// MaxImageBytes is the largest decoded upload payload accepted by Scribe.
	MaxImageBytes int64 = 100 << 20
	// MultipartOverheadBytes leaves bounded room for MIME boundaries, file
	// metadata, and small scalar fields without accepting another image-sized
	// payload.
	MultipartOverheadBytes int64 = 1 << 20
	MaxMultipartBodyBytes        = MaxImageBytes + MultipartOverheadBytes
	// MultipartMemoryBytes keeps large image parts on disk.
	MultipartMemoryBytes int64 = 8 << 20
	// MaxImageDimension and MaxImagePixels bound decoded work independently of
	// compressed upload size. Every image-processing service uses this same
	// envelope so moving work across a service boundary cannot change its cost.
	MaxImageDimension = 30_000
	MaxImagePixels    = int64(100_000_000)
)

func ValidateImageSize(size int64) error {
	if size < 0 {
		return fmt.Errorf("image size cannot be negative")
	}
	if size > MaxImageBytes {
		return fmt.Errorf("image exceeds %d byte limit", MaxImageBytes)
	}
	return nil
}

func ValidateImageDimensions(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("image dimensions must be positive")
	}
	if width > MaxImageDimension || height > MaxImageDimension || int64(width)*int64(height) > MaxImagePixels {
		return fmt.Errorf("image dimensions %dx%d exceed processing limits", width, height)
	}
	return nil
}
