package server

import (
	"os"
	"testing"

	"github.com/lehigh-university-libraries/scribe/internal/config"
	"github.com/lehigh-university-libraries/scribe/internal/safehttp"
)

func TestMain(m *testing.M) {
	_ = os.Setenv(safehttp.AllowPrivateFetchesEnv, "true")
	config.Init(config.Runtime{Config: config.Config{
		PublicBaseURL: "https://scribe.test",
		IIIF: config.IIIFConfig{
			Base:         "https://scribe.test/iiif/3",
			InternalBase: "http://triplet:8080/iiif/3",
			SourceBase:   "http://api:8080/static/uploads",
		},
		Annotation: config.AnnotationConfig{
			APIBase:                         "/",
			APIInternalBase:                 "http://api:8080",
			TripletPresentationBase:         "https://scribe.test/presentation/v3",
			TripletPresentationInternalBase: "http://triplet:8080/presentation/v3",
			TripletPresentationWriteToken:   "test-triplet-presentation-token-32-bytes",
		},
	}})
	os.Exit(m.Run())
}
