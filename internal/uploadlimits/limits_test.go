package uploadlimits

import "testing"

func TestInternalMultipartEnvelopeCoversPublicImageContract(t *testing.T) {
	t.Parallel()

	if MaxImageBytes <= 64<<20 {
		t.Fatalf("MaxImageBytes = %d; must cover uploads above the former 64 MiB boundary", MaxImageBytes)
	}
	if MaxMultipartBodyBytes <= MaxImageBytes || MaxMultipartBodyBytes-MaxImageBytes != MultipartOverheadBytes {
		t.Fatalf("multipart envelope = %d image + %d overhead", MaxImageBytes, MaxMultipartBodyBytes-MaxImageBytes)
	}
	for _, size := range []int64{64 << 20, (64 << 20) + 1, MaxImageBytes} {
		if err := ValidateImageSize(size); err != nil {
			t.Fatalf("ValidateImageSize(%d) error = %v", size, err)
		}
	}
	for _, size := range []int64{-1, MaxImageBytes + 1} {
		if err := ValidateImageSize(size); err == nil {
			t.Fatalf("ValidateImageSize(%d) succeeded", size)
		}
	}
}

func TestDecodedImageEnvelopeIsSharedAndOverflowSafe(t *testing.T) {
	t.Parallel()

	for _, dimensions := range [][2]int{{1, 1}, {MaxImageDimension, 1}, {10_000, 10_000}} {
		if err := ValidateImageDimensions(dimensions[0], dimensions[1]); err != nil {
			t.Fatalf("ValidateImageDimensions(%d, %d) error = %v", dimensions[0], dimensions[1], err)
		}
	}
	for _, dimensions := range [][2]int{{0, 1}, {1, 0}, {-1, 1}, {MaxImageDimension + 1, 1}, {10_001, 10_000}} {
		if err := ValidateImageDimensions(dimensions[0], dimensions[1]); err == nil {
			t.Fatalf("ValidateImageDimensions(%d, %d) succeeded", dimensions[0], dimensions[1])
		}
	}
}
