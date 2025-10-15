package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/lehigh-university-libraries/hOCRedit/internal/handlers"
	"github.com/lehigh-university-libraries/hOCRedit/internal/utils"
)

func main() {
	err := godotenv.Load()
	if err != nil {
		slog.Warn("Error loading .env file", "err", err)
	}

	handler := handlers.New()

	// Set up routes
	http.HandleFunc("/api/sessions", handler.HandleSessions)
	http.HandleFunc("/api/sessions/", handler.HandleSessionDetail)
	http.HandleFunc("/api/upload", handler.HandleUpload)
	http.HandleFunc("/api/ocr", handler.HandleOCR)
	http.HandleFunc("/api/hocr/parse", handler.HandleHOCRParse)
	http.HandleFunc("/api/hocr/update", handler.HandleHOCRUpdate)
	http.HandleFunc("/", handler.HandleStatic)
	http.HandleFunc("/healthcheck", func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("OK"))
		if err != nil {
			slog.Error("Unable to write healthcheck", "err", err)
			os.Exit(1)
		}
	})
	addr := ":8888"
	slog.Info("hOCR Editor interface available", "addr", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		utils.ExitOnError("Server failed to start", err)
	}
}
