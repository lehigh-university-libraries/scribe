// Command ocr-matrix prints the GitHub Actions build matrix for every OCR
// image declared in config/ocr.yaml:
//
//	go run ./cmd/ocr-matrix -project my-project -tag main
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lehigh-university-libraries/scribe/internal/ocrimages"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "ocr-matrix: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("ocr-matrix", flag.ContinueOnError)
	config := flags.String("config", "config/ocr.yaml", "OCR config path")
	project := flags.String("project", os.Getenv("GCLOUD_PROJECT"), "GCP project ID")
	tag := flags.String("tag", "main", "image tag")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return fmt.Errorf("-project or GCLOUD_PROJECT is required")
	}
	cfg, err := ocrimages.Load(*config)
	if err != nil {
		return err
	}
	entries, err := ocrimages.Matrix(cfg, "us-docker.pkg.dev/"+*project+"/internal", *tag)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{"include": entries})
}
