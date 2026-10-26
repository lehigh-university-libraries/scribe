package ocrimages

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOllamaImageRejectsMissingOrUnverifiedModels(t *testing.T) {
	source, err := os.ReadFile("../../terraform/modules/ollama-cloud-run/image/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load("../../config/ocr.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(source), "ARG OLLAMA_BASE_IMAGE="+catalog.Ollama.BaseImage+"\n") {
		t.Fatal("Ollama catalog and Dockerfile base images differ")
	}
	var bake string
	for _, instruction := range strings.Split(string(source), "\nRUN ")[1:] {
		if strings.Contains(instruction, "server_pid=$!") {
			bake = strings.ReplaceAll(strings.SplitN(instruction, "\n\n", 2)[0], "\\\n", "\n")
		}
	}
	if bake == "" {
		t.Fatal("model baking instruction missing")
	}
	for _, tc := range []struct {
		name, pullExit, list string
		wantSuccess          bool
	}{
		{"verified model", "0", "glm-ocr:bf16 aaaaaaaaaaaa", true},
		{"pull rejected", "1", "glm-ocr:bf16 aaaaaaaaaaaa", false},
		{"model absent", "0", "", false},
		{"digest mismatch", "0", "glm-ocr:bf16 bbbbbbbbbbbb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := "#!/bin/sh\ncase \"$1\" in\nserve) exec /bin/sleep 30;;\npull) exit \"$PULL_EXIT\";;\nlist) printf 'NAME ID\\n%s\\n' \"$MODEL_LIST\";;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "ollama"), []byte(stub), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("sh", "-c", bake)
			command.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "OLLAMA_MODEL=glm-ocr:bf16", "OLLAMA_MODEL_DIGEST="+strings.Repeat("a", 64), "PULL_EXIT="+tc.pullExit, "MODEL_LIST="+tc.list)
			output, err := command.CombinedOutput()
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("image bake = %v, want success %v: %s", err, tc.wantSuccess, output)
			}
		})
	}
}
