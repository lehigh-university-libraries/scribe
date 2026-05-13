package server

import (
	"os"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
)

func TestMain(m *testing.M) {
	_ = os.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	os.Exit(m.Run())
}
