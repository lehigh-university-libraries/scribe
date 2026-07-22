package imagemagick

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
)

var resourceLimits = []string{
	"-limit", "thread", "2",
	"-limit", "time", "60",
	"-limit", "width", "30000",
	"-limit", "height", "30000",
	"-limit", "area", "100MP",
	"-limit", "memory", "256MiB",
	"-limit", "map", "512MiB",
	"-limit", "disk", "1GiB",
}

var (
	resolveConvertOnce sync.Once
	resolveConvertPath string
	resolveConvertErr  error
)

// ConvertCommandContext creates a resource-limited ImageMagick process whose
// lifetime is bound to the caller. OCR cancellation must terminate local image
// work instead of waiting for the command timeout.
func ConvertCommandContext(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resolveConvertOnce.Do(func() {
		for _, candidate := range []string{"magick", "convert"} {
			path, err := exec.LookPath(candidate)
			if err == nil {
				resolveConvertPath = path
				return
			}
		}
		resolveConvertErr = fmt.Errorf("imagemagick convert command not found; tried magick and convert")
	})
	if resolveConvertErr != nil {
		return nil, resolveConvertErr
	}
	return exec.CommandContext(ctx, resolveConvertPath, limitedArguments(args)...), nil // #nosec G204 -- ImageMagick is invoked directly without a shell and the executable path is resolved from fixed binary names.
}

func limitedArguments(args []string) []string {
	limited := make([]string, 0, len(resourceLimits)+len(args))
	limited = append(limited, resourceLimits...)
	limited = append(limited, args...)
	return limited
}
