package worddetection

import (
	"fmt"
	"math"
)

// ValidateBoxes admits provider output before any grouping, cropping, or paid
// transcription fan-out. Both local and remote detector adapters pass through
// this boundary.
func ValidateBoxes(words []WordBox, imageWidth, imageHeight, maxBoxes int) error {
	if imageWidth <= 0 || imageHeight <= 0 {
		return fmt.Errorf("image dimensions must be positive")
	}
	if maxBoxes <= 0 || len(words) > maxBoxes {
		return fmt.Errorf("detector returned %d boxes; maximum is %d", len(words), maxBoxes)
	}
	for index, box := range words {
		if box.X < 0 || box.Y < 0 || box.Width <= 0 || box.Height <= 0 || box.X >= imageWidth || box.Y >= imageHeight || box.Width > imageWidth-box.X || box.Height > imageHeight-box.Y {
			return fmt.Errorf("detector box %d geometry (%d,%d,%d,%d) is outside %dx%d image", index, box.X, box.Y, box.Width, box.Height, imageWidth, imageHeight)
		}
		if math.IsNaN(box.Confidence) || math.IsInf(box.Confidence, 0) {
			return fmt.Errorf("detector box %d confidence must be finite", index)
		}
	}
	return nil
}
