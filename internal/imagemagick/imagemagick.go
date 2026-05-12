package imagemagick

import (
	"fmt"
	"os/exec"
	"sync"
)

var (
	resolveConvertOnce sync.Once
	resolveConvertPath string
	resolveConvertErr  error
)

func ConvertCommand(args ...string) (*exec.Cmd, error) {
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
	return exec.Command(resolveConvertPath, args...), nil // #nosec G204 -- ImageMagick is invoked directly without a shell and the executable path is resolved from fixed binary names.
}
