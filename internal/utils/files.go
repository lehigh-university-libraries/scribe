package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/lehigh-university-libraries/scribe/internal/safefile"
	"github.com/lehigh-university-libraries/scribe/internal/safelog"
)

func CalculateFileHash(filePath string) (string, error) {
	file, err := safefile.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func CalculateDataHash(data []byte) string {
	hash := sha256.New()
	hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func RespondWithError(w http.ResponseWriter, message string, statusCode int) {
	w.WriteHeader(statusCode)
	response := map[string]string{
		"error": message,
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("Failed to encode error response", "error_type", safelog.ErrorType(err))
	}
}
