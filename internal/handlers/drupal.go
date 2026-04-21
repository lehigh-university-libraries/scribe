package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/scribe/internal/config"
)

// DrupalFileObject represents a single file object from Drupal
type DrupalFileObject struct {
	URI      string `json:"uri"`
	TermName string `json:"term_name"`
	TID      string `json:"tid"`
	NID      string `json:"nid"`
	ViewNode string `json:"view_node"`
}

// DrupalHOCRData represents the JSON response from Drupal HOCR endpoint (array of file objects)
type DrupalHOCRData []DrupalFileObject

// createSessionFromDrupalNode creates a session from a Drupal node ID
func (h *Handler) createSessionFromDrupalNode(nid string) (string, error) {
	drupalData, err := h.fetchDrupalData(nid)
	if err != nil {
		return "", err
	}

	serviceFile, hocrFile, err := h.extractDrupalFiles(drupalData)
	if err != nil {
		return "", err
	}

	imageURL, hocrUploadURL := h.buildDrupalURLs(serviceFile, hocrFile, nid)

	// Create session based on whether we have existing hOCR
	var sessionID string
	if strings.Contains(hocrFile.URI, "gcloud") {
		sessionID, err = h.createSessionFromDrupalWithExistingHOCR(imageURL, hocrFile.ViewNode+hocrFile.URI, nid)
	} else {
		sessionID, err = h.createSessionFromDrupalWithNewHOCR(imageURL, nid)
	}

	if err != nil {
		return "", fmt.Errorf("failed to create session from Drupal: %w", err)
	}

	// Add Drupal metadata to session
	h.addDrupalMetadataToSession(sessionID, nid, hocrUploadURL)

	return sessionID, nil
}

func (h *Handler) fetchDrupalData(nid string) (DrupalHOCRData, error) {
	drupalURL := config.Get().Config.Drupal.HOCRURLTemplate
	if drupalURL == "" {
		return nil, fmt.Errorf("drupal.hocr_url_template is not configured")
	}

	requestURL := fmt.Sprintf(drupalURL, nid)
	slog.Info("Fetching Drupal HOCR data", "nid", nid, "url", requestURL)

	resp, err := http.Get(requestURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Drupal data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("drupal returned HTTP %d", resp.StatusCode)
	}

	var drupalData DrupalHOCRData
	if err := json.NewDecoder(resp.Body).Decode(&drupalData); err != nil {
		return nil, fmt.Errorf("failed to parse Drupal JSON: %w", err)
	}

	if len(drupalData) == 0 {
		return nil, fmt.Errorf("no file objects provided by Drupal")
	}

	return drupalData, nil
}

func (h *Handler) extractDrupalFiles(drupalData DrupalHOCRData) (*DrupalFileObject, *DrupalFileObject, error) {

	var serviceFile, hocrFile *DrupalFileObject
	for i, fileObj := range drupalData {
		switch fileObj.TermName {
		case "Service File":
			serviceFile = &drupalData[i]
		case "hOCR":
			hocrFile = &drupalData[i]
		}
	}

	if serviceFile == nil {
		return nil, nil, fmt.Errorf("no Service File found in Drupal response")
	}

	if hocrFile == nil {
		return nil, nil, fmt.Errorf("no hOCR file found in Drupal response")
	}

	return serviceFile, hocrFile, nil
}

func (h *Handler) buildDrupalURLs(serviceFile, hocrFile *DrupalFileObject, nid string) (string, string) {
	drupalURL := config.Get().Config.Drupal.HOCRURLTemplate
	baseUrl := strings.Replace(drupalURL, "/node/%s/hocr", "", 1)

	imageURL := baseUrl + serviceFile.ViewNode + serviceFile.URI
	hocrUploadURL := fmt.Sprintf("%s/node/%s%s/media/file/%s", baseUrl, nid, serviceFile.ViewNode, hocrFile.TID)

	slog.Info("Retrieved Drupal data", "nid", nid, "image_url", imageURL, "hocr_upload", hocrUploadURL)
	return imageURL, hocrUploadURL
}

func (h *Handler) addDrupalMetadataToSession(sessionID, nid, hocrUploadURL string) {
	session, exists := h.sessionStore.Get(sessionID)
	if exists {
		session.Config.Prompt = fmt.Sprintf("Drupal Node %s - %s", nid, session.Config.Prompt)

		if len(session.Images) > 0 {
			session.Images[0].DrupalUploadURL = hocrUploadURL
			session.Images[0].DrupalNid = nid
		}

		h.sessionStore.Set(sessionID, session)
	}
}

func (h *Handler) createSessionFromDrupalWithExistingHOCR(imageURL, hocrURL, nid string) (string, error) {
	result, err := h.processImageFromURL(imageURL)
	if err != nil {
		return "", err
	}

	// Download and override with existing hOCR
	hocrData, err := h.downloadHOCR(hocrURL)
	if err != nil {
		return "", err
	}
	result.HOCRXML = string(hocrData)

	slog.Info("Using existing hOCR from Drupal", "nid", nid, "hocr_url", hocrURL)

	// Create session with Drupal prefix
	filename := h.extractFilenameFromURL(imageURL, result.MD5Hash)
	sessionID := fmt.Sprintf("drupal_%s_%s_%d", nid, filename, time.Now().Unix())

	config := SessionConfig{
		Model:       "drupal_existing_hocr",
		Prompt:      "Using existing hOCR from Drupal",
		Temperature: 0.0,
	}

	session := h.createImageSession(sessionID, result, config)
	h.sessionStore.Set(sessionID, session)

	slog.Info("Session created from Drupal with existing hOCR", "session_id", sessionID, "nid", nid)
	return sessionID, nil
}

func (h *Handler) downloadHOCR(hocrURL string) ([]byte, error) {
	resp, err := http.Get(hocrURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download existing hOCR: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download hOCR: HTTP %d", resp.StatusCode)
	}

	hocrData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read hOCR data: %w", err)
	}

	return hocrData, nil
}

func (h *Handler) createSessionFromDrupalWithNewHOCR(imageURL, nid string) (string, error) {
	result, err := h.processImageFromURL(imageURL)
	if err != nil {
		return "", err
	}

	filename := h.extractFilenameFromURL(imageURL, result.MD5Hash)
	sessionID := fmt.Sprintf("drupal_%s_%s_%d", nid, filename, time.Now().Unix())

	config := SessionConfig{
		Model:       "custom_with_chatgpt",
		Prompt:      "Custom word detection + ChatGPT OCR with hOCR conversion for Drupal",
		Temperature: 0.0,
	}

	session := h.createImageSession(sessionID, result, config)
	h.sessionStore.Set(sessionID, session)

	slog.Info("Session created from Drupal with new hOCR", "session_id", sessionID, "nid", nid)
	return sessionID, nil
}
