package hocr

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lehigh-university-libraries/hOCRedit/internal/models"
	"github.com/lehigh-university-libraries/hOCRedit/internal/utils"
)

type ChatGPTRequest struct {
	Model       string           `json:"model"`
	Temperature float64          `json:"temperature,omitempty"`
	Messages    []ChatGPTMessage `json:"messages"`
}

type ChatGPTMessage struct {
	Role    string           `json:"role"`
	Content []ChatGPTContent `json:"content"`
}

type ChatGPTContent struct {
	Type     string           `json:"type"`
	Text     string           `json:"text,omitempty"`
	ImageURL *ChatGPTImageURL `json:"image_url,omitempty"`
}

type ChatGPTImageURL struct {
	URL string `json:"url"`
}

type ChatGPTResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (s *Service) createStitchedImageWithHOCRMarkup(imagePath string, response models.OCRResponse) (string, error) {
	tempDir := "/tmp"
	baseName := strings.TrimSuffix(filepath.Base(imagePath), filepath.Ext(imagePath))
	stitchedPath := filepath.Join(tempDir, fmt.Sprintf("stitched_%s_%d.png", baseName, time.Now().Unix()))

	var componentPaths []string

	if len(response.Responses) == 0 || response.Responses[0].FullTextAnnotation == nil {
		return "", fmt.Errorf("no text annotation in response")
	}

	wordIndex := 0
	for _, page := range response.Responses[0].FullTextAnnotation.Pages {
		for _, block := range page.Blocks {
			for _, paragraph := range block.Paragraphs {
				for _, word := range paragraph.Words {
					if len(word.BoundingBox.Vertices) < 4 {
						continue
					}

					bbox := word.BoundingBox

					// Create hOCR line opening tag
					lineTag := fmt.Sprintf(`<span class='ocrx_line' id='line_%d' title='bbox %d %d %d %d'>`,
						wordIndex+1,
						bbox.Vertices[0].X, bbox.Vertices[0].Y,
						bbox.Vertices[2].X, bbox.Vertices[2].Y)
					lineTagPath, err := s.createTextImage(lineTag, tempDir, fmt.Sprintf("line_%d", wordIndex))
					if err != nil {
						utils.ExitOnError("Unable to add line hOCR text to stitched image", err)
					}

					componentPaths = append(componentPaths, lineTagPath)

					// Create hOCR word opening tag
					wordTag := fmt.Sprintf(`<span class='ocrx_word' id='word_%d' title='bbox %d %d %d %d'>`,
						wordIndex+1,
						bbox.Vertices[0].X, bbox.Vertices[0].Y,
						bbox.Vertices[2].X, bbox.Vertices[2].Y)
					wordTagPath, err := s.createTextImage(wordTag, tempDir, fmt.Sprintf("word_%d", wordIndex))
					if err != nil {
						utils.ExitOnError("Unable to add word hOCR text to stitched image", err)
					}
					componentPaths = append(componentPaths, wordTagPath)

					// Extract the actual word image
					wordImagePath, err := s.extractWordImage(imagePath, bbox, tempDir, wordIndex)
					if err != nil {
						utils.ExitOnError("Unable to add image cutout to stitched image", err)
					}
					componentPaths = append(componentPaths, wordImagePath)

					// Create closing tags
					wordClosePath, err := s.createTextImage("</span>", tempDir, fmt.Sprintf("word_close_%d", wordIndex))
					if err != nil {
						utils.ExitOnError("Unable to add closing word span to stitched image", err)
					}
					componentPaths = append(componentPaths, wordClosePath)

					lineClosePath, err := s.createTextImage("</span>", tempDir, fmt.Sprintf("line_close_%d", wordIndex))
					if err != nil {
						utils.ExitOnError("Unable to add closing line span to stitched image", err)
					}
					componentPaths = append(componentPaths, lineClosePath)

					wordIndex++
				}
			}
		}
	}

	if len(componentPaths) == 0 {
		return "", fmt.Errorf("no valid components were created")
	}

	// Stitch all components together vertically
	args := append(componentPaths, "-append", stitchedPath)
	cmd := exec.Command("magick", args...)
	err := cmd.Run()

	// Clean up component images
	for _, componentPath := range componentPaths {
		os.Remove(componentPath)
	}

	if err != nil {
		return "", fmt.Errorf("failed to stitch components: %w", err)
	}

	return stitchedPath, nil
}

func (s *Service) createTextImage(text, tempDir, filename string) (string, error) {
	outputPath := filepath.Join(tempDir, fmt.Sprintf("%s_%d.png", filename, time.Now().Unix()))

	cmd := exec.Command("magick",
		"-size", "2000x60",
		"xc:white",
		"-fill", "black",
		"-font", "DejaVu-Sans-Mono",
		"-pointsize", "24",
		"-draw", fmt.Sprintf(`text 10,40 "%s"`, text),
		outputPath)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create text image: %w", err)
	}

	return outputPath, nil
}

func (s *Service) extractWordImage(imagePath string, bbox models.BoundingPoly, tempDir string, wordIndex int) (string, error) {
	if len(bbox.Vertices) < 4 {
		return "", fmt.Errorf("invalid bounding box")
	}

	minX := bbox.Vertices[0].X
	minY := bbox.Vertices[0].Y
	maxX := bbox.Vertices[2].X
	maxY := bbox.Vertices[2].Y

	width := maxX - minX
	height := maxY - minY

	if width <= 0 || height <= 0 {
		return "", fmt.Errorf("invalid dimensions")
	}

	// Add padding
	padding := 3
	cropX := max(0, minX-padding)
	cropY := max(0, minY-padding)
	cropWidth := width + 2*padding
	cropHeight := height + 2*padding

	outputPath := filepath.Join(tempDir, fmt.Sprintf("word_img_%d_%d.png", wordIndex, time.Now().Unix()))

	cmd := exec.Command("magick", imagePath,
		"-crop", fmt.Sprintf("%dx%d+%d+%d", cropWidth, cropHeight, cropX, cropY),
		"+repage",
		outputPath)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to extract word image: %w", err)
	}

	return outputPath, nil
}

func (s *Service) transcribeWithChatGPT(imagePath string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY environment variable not set")
	}

	// Encode image as base64
	imageData, err := os.ReadFile(imagePath)
	if err != nil {
		return "", fmt.Errorf("failed to read image: %w", err)
	}
	imageBase64 := base64.StdEncoding.EncodeToString(imageData)

	// Create ChatGPT request
	request := ChatGPTRequest{
		Model: s.getModel(),
		Messages: []ChatGPTMessage{
			{
				Role: "user",
				Content: []ChatGPTContent{
					{
						Type: "text",
						Text: `Read and transcribe all the hOCR markup overlaid on this image.
You will see hOCR tags like:
<span class='ocrx_line' id='line_X' title='bbox x y w h'>
<span class='ocrx_word' id='word_X' title='bbox x y w h'>
[word image that needs transcription]
</span>
</span>

Transcribe BOTH the hOCR tags AND the text content inside them.
For each word image, read the text and include it between the word tags.
If a word image has no legible text, omit that word's span entirely.
IMPORTANT: If the transcribed text contains special characters like &, <, >, ", or ', 
please replace them with their XML entities: &amp; &lt; &gt; &quot; &#39;
Return only the hOCR markup with transcribed text content.`,
					},
					{
						Type: "image_url",
						ImageURL: &ChatGPTImageURL{
							URL: fmt.Sprintf("data:image/png;base64,%s", imageBase64),
						},
					},
				},
			},
		},
	}

	return s.callChatGPT(request)
}

func (s *Service) callChatGPT(request ChatGPTRequest) (string, error) {
	requestBody, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENAI_API_KEY"))

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ChatGPT API returned status %d: %s", resp.StatusCode, string(body))
	}

	var chatGPTResponse ChatGPTResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatGPTResponse); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if len(chatGPTResponse.Choices) == 0 {
		return "", fmt.Errorf("no response from ChatGPT")
	}

	content := strings.TrimSpace(chatGPTResponse.Choices[0].Message.Content)
	content = s.cleanChatGPTResponse(content)

	return content, nil
}

func (s *Service) cleanChatGPTResponse(content string) string {
	// Clean up the ChatGPT response to fix common XML issues
	result := content

	// Handle standalone & characters that aren't part of valid entities
	// Replace & with &amp; unless it's already part of a valid entity
	result = s.fixAmpersands(result)

	// Clean up any other problematic characters in text content
	result = s.escapeTextContent(result)

	return result
}

func (s *Service) fixAmpersands(content string) string {
	// Replace & with &amp; unless it's already part of a valid XML entity
	validEntities := []string{"&amp;", "&lt;", "&gt;", "&quot;", "&apos;", "&#39;"}

	result := content
	lines := strings.Split(result, "\n")
	var cleanLines []string

	for _, line := range lines {
		cleanLine := line

		// Find all & characters
		for i := 0; i < len(cleanLine); i++ {
			if cleanLine[i] == '&' {
				// Check if this is part of a valid entity
				isValidEntity := false
				for _, entity := range validEntities {
					if i+len(entity) <= len(cleanLine) && cleanLine[i:i+len(entity)] == entity {
						isValidEntity = true
						i += len(entity) - 1 // Skip past this entity
						break
					}
				}

				// Check for numeric entities like &#39;
				if !isValidEntity && i+2 < len(cleanLine) && cleanLine[i+1] == '#' {
					// Look for numeric entity pattern &#digits;
					j := i + 2
					for j < len(cleanLine) && cleanLine[j] >= '0' && cleanLine[j] <= '9' {
						j++
					}
					if j < len(cleanLine) && cleanLine[j] == ';' {
						isValidEntity = true
						i = j // Skip past this entity
					}
				}

				if !isValidEntity {
					// Replace this & with &amp;
					cleanLine = cleanLine[:i] + "&amp;" + cleanLine[i+1:]
					i += 4 // Skip past the inserted &amp;
				}
			}
		}

		cleanLines = append(cleanLines, cleanLine)
	}

	return strings.Join(cleanLines, "\n")
}

func (s *Service) escapeTextContent(content string) string {
	// This function looks for text content within span tags and escapes any remaining problematic characters
	lines := strings.Split(content, "\n")
	var cleanLines []string

	for _, line := range lines {
		if strings.Contains(line, "<span") && strings.Contains(line, "</span>") {
			// Process span lines to escape text content
			cleaned := s.escapeTextInSpans(line)
			cleanLines = append(cleanLines, cleaned)
		} else {
			cleanLines = append(cleanLines, line)
		}
	}

	return strings.Join(cleanLines, "\n")
}

func (s *Service) escapeTextInSpans(line string) string {
	// Split by </span> to process each span element
	parts := strings.Split(line, "</span>")

	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		lastGT := strings.LastIndex(part, ">")
		if lastGT >= 0 && lastGT < len(part)-1 {
			before := part[:lastGT+1]
			text := part[lastGT+1:]

			// Only escape < and > that aren't already escaped and aren't part of valid entities
			text = strings.ReplaceAll(text, "<", "&lt;")
			text = strings.ReplaceAll(text, ">", "&gt;")

			parts[i] = before + text
		}
	}

	return strings.Join(parts, "</span>")
}

func (s *Service) getModel() string {
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		return "gpt-4o"
	}
	return model
}

func (s *Service) convertToBasicHOCR(response models.OCRResponse) string {
	var lines []string
	var width, height int

	if len(response.Responses) == 0 || response.Responses[0].FullTextAnnotation == nil {
		return s.wrapInHOCRDocument("", 0, 0)
	}

	wordIndex := 0
	for _, page := range response.Responses[0].FullTextAnnotation.Pages {
		width = page.Width
		height = page.Height
		for _, block := range page.Blocks {
			for _, paragraph := range block.Paragraphs {
				for _, word := range paragraph.Words {
					if len(word.BoundingBox.Vertices) >= 4 && len(word.Symbols) > 0 {
						bbox := word.BoundingBox
						text := html.EscapeString(word.Symbols[0].Text) // Use detected text with XML escaping
						line := fmt.Sprintf(`<span class='ocrx_line' id='line_%d' title='bbox %d %d %d %d'><span class='ocrx_word' id='word_%d' title='bbox %d %d %d %d'>%s</span></span>`,
							wordIndex+1,
							bbox.Vertices[0].X, bbox.Vertices[0].Y,
							bbox.Vertices[2].X, bbox.Vertices[2].Y,
							wordIndex+1,
							bbox.Vertices[0].X, bbox.Vertices[0].Y,
							bbox.Vertices[2].X, bbox.Vertices[2].Y,
							text)
						lines = append(lines, line)
						wordIndex++
					}
				}
			}
		}
	}

	return s.wrapInHOCRDocument(strings.Join(lines, "\n"), width, height)
}
