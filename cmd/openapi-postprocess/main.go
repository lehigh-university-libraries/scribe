// Command openapi-postprocess adds Scribe's protobuf authorization contract to
// the OpenAPI document emitted by protoc-gen-connect-openapi.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lehigh-university-libraries/scribe/internal/openapicontract"
	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	_ "github.com/lehigh-university-libraries/scribe/proto/scribe/v1"
	"google.golang.org/protobuf/reflect/protoregistry"
)

const maxGeneratedOpenAPIBytes = 16 << 20

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "openapi-postprocess: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: openapi-postprocess <generated-openapi.yaml>")
	}
	path := filepath.Clean(args[0])
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat generated OpenAPI: %w", err)
	}
	if info.Size() > maxGeneratedOpenAPIBytes {
		return fmt.Errorf("generated OpenAPI exceeds %d bytes", maxGeneratedOpenAPIBytes)
	}
	source, err := safefile.ReadFileLimit(path, maxGeneratedOpenAPIBytes)
	if err != nil {
		return fmt.Errorf("read generated OpenAPI: %w", err)
	}
	procedures, err := openapicontract.ScribeProcedures(protoregistry.GlobalFiles)
	if err != nil {
		return err
	}
	output, err := openapicontract.Rewrite(source, procedures)
	if err != nil {
		return err
	}
	return replaceFile(path, output, info.Mode().Perm())
}

func replaceFile(path string, content []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".scribe-openapi-*")
	if err != nil {
		return fmt.Errorf("create temporary OpenAPI: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath) // #nosec G703 -- temporaryPath comes directly from os.CreateTemp.
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set temporary OpenAPI permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write temporary OpenAPI: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary OpenAPI: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary OpenAPI: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace generated OpenAPI: %w", err)
	}
	return nil
}
