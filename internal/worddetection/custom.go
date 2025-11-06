package worddetection

import (
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CustomProvider implements word detection using custom flood-fill algorithm
type CustomProvider struct{}

// NewCustom creates a new custom word detection provider
func NewCustom() *CustomProvider {
	return &CustomProvider{}
}

// Name returns the provider name
func (p *CustomProvider) Name() string {
	return "custom"
}

// DetectWords detects word bounding boxes using custom flood-fill algorithm
func (p *CustomProvider) DetectWords(ctx context.Context, imagePath string) ([]WordBox, error) {
	// Preprocess the image
	processedPath, err := p.preprocessImage(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to preprocess image: %w", err)
	}
	defer os.Remove(processedPath)

	// Load processed image
	file, err := os.Open(processedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open processed image: %w", err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("failed to decode processed image: %w", err)
	}

	bounds := img.Bounds()
	imgWidth := bounds.Dx()
	imgHeight := bounds.Dy()

	// Find connected components (potential words)
	components := p.findWordComponents(img, imgWidth, imgHeight)

	// Filter and refine components to get word boxes
	wordBoxes := p.refineComponents(components, imgWidth, imgHeight)

	return wordBoxes, nil
}

// preprocessImage preprocesses the image for better word detection
func (p *CustomProvider) preprocessImage(imagePath string) (string, error) {
	tempDir := "/tmp"
	baseName := strings.TrimSuffix(filepath.Base(imagePath), filepath.Ext(imagePath))
	processedPath := filepath.Join(tempDir, fmt.Sprintf("processed_custom_%s_%d.jpg", baseName, time.Now().Unix()))

	// Preprocess: grayscale, enhance contrast, sharpen, threshold
	cmd := exec.Command("magick", imagePath,
		"-colorspace", "Gray",
		"-contrast-stretch", "0.15x0.05%",
		"-sharpen", "0x1",
		"-morphology", "close", "rectangle:2x1",
		"-threshold", "75%",
		processedPath)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("imagemagick preprocessing failed: %w", err)
	}

	return processedPath, nil
}

// findWordComponents finds connected components that could be words
func (p *CustomProvider) findWordComponents(img image.Image, imgWidth, imgHeight int) []WordBox {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	visited := make([][]bool, height)
	for i := range visited {
		visited[i] = make([]bool, width)
	}

	var components []WordBox

	// Find all connected components using flood fill
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if !visited[y][x] && p.isTextPixel(img.At(x, y)) {
				minX, minY, maxX, maxY := x, y, x, y
				p.floodFill(img, visited, x, y, &minX, &minY, &maxX, &maxY)

				// Filter by size to get potential words
				w := maxX - minX + 1
				h := maxY - minY + 1
				if p.isValidWordSize(w, h, imgWidth, imgHeight) {
					components = append(components, WordBox{
						X:          minX,
						Y:          minY,
						Width:      w,
						Height:     h,
						Text:       fmt.Sprintf("custom_word_%d", len(components)+1),
						Confidence: 90.0,
					})
				}
			}
		}
	}

	return components
}

// floodFill performs iterative flood fill to find connected text pixels
func (p *CustomProvider) floodFill(img image.Image, visited [][]bool, startX, startY int, minX, minY, maxX, maxY *int) {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	// Use a stack for iterative flood fill
	type point struct{ x, y int }
	stack := []point{{startX, startY}}

	// 8-directional neighbors
	directions := []point{{-1, -1}, {-1, 0}, {-1, 1}, {0, -1}, {0, 1}, {1, -1}, {1, 0}, {1, 1}}

	for len(stack) > 0 {
		// Pop from stack
		pt := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		x, y := pt.x, pt.y

		// Check bounds
		if x < 0 || x >= width || y < 0 || y >= height {
			continue
		}

		// Skip if already visited or not a text pixel
		if visited[y][x] || !p.isTextPixel(img.At(x, y)) {
			continue
		}

		// Mark as visited
		visited[y][x] = true

		// Update bounding box
		if x < *minX {
			*minX = x
		}
		if x > *maxX {
			*maxX = x
		}
		if y < *minY {
			*minY = y
		}
		if y > *maxY {
			*maxY = y
		}

		// Add all 8 neighbors to stack
		for _, dir := range directions {
			stack = append(stack, point{x + dir.x, y + dir.y})
		}
	}
}

// isTextPixel determines if a pixel is likely part of text (dark pixel)
func (p *CustomProvider) isTextPixel(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	gray := (r + g + b) / 3
	return gray < 32768 // Dark pixels are considered text
}

// isValidWordSize checks if a component size is reasonable for a word
func (p *CustomProvider) isValidWordSize(w, h, imgWidth, imgHeight int) bool {
	minWidth, minHeight := 8, 10
	maxWidth := imgWidth / 2
	maxHeight := imgHeight / 5

	return w >= minWidth && h >= minHeight && w <= maxWidth && h <= maxHeight
}

// refineComponents refines detected components into word boxes
func (p *CustomProvider) refineComponents(components []WordBox, imgWidth, imgHeight int) []WordBox {
	if len(components) == 0 {
		return components
	}

	// Sort components for processing (top to bottom, left to right)
	sort.Slice(components, func(i, j int) bool {
		if abs(components[i].Y-components[j].Y) < 10 {
			return components[i].X < components[j].X
		}
		return components[i].Y < components[j].Y
	})

	// Merge nearby components that likely belong to the same word
	mergedWords := p.mergeNearbyComponents(components)

	return mergedWords
}

// mergeNearbyComponents merges components that are close together into single words
func (p *CustomProvider) mergeNearbyComponents(components []WordBox) []WordBox {
	if len(components) <= 1 {
		return components
	}

	var mergedWords []WordBox
	currentGroup := []WordBox{components[0]}

	for i := 1; i < len(components); i++ {
		component := components[i]
		lastInGroup := currentGroup[len(currentGroup)-1]

		// Check if this component should be merged with the current group
		if p.shouldMerge(lastInGroup, component) {
			currentGroup = append(currentGroup, component)
		} else {
			// Finish current group and start new one
			mergedWord := p.mergeGroup(currentGroup)
			mergedWords = append(mergedWords, mergedWord)
			currentGroup = []WordBox{component}
		}
	}

	// Don't forget the last group
	if len(currentGroup) > 0 {
		mergedWord := p.mergeGroup(currentGroup)
		mergedWords = append(mergedWords, mergedWord)
	}

	return mergedWords
}

// shouldMerge determines if two components should be merged into one word
func (p *CustomProvider) shouldMerge(a, b WordBox) bool {
	horizontalGap := b.X - (a.X + a.Width)
	verticalOverlap := b.Y+b.Height >= a.Y && b.Y <= a.Y+a.Height

	maxGap := max(a.Height, b.Height) / 3
	return horizontalGap >= 0 && horizontalGap <= maxGap && verticalOverlap
}

// mergeGroup merges a group of components into a single word box
func (p *CustomProvider) mergeGroup(group []WordBox) WordBox {
	if len(group) == 1 {
		return group[0]
	}

	minX, minY := group[0].X, group[0].Y
	maxX, maxY := group[0].X+group[0].Width, group[0].Y+group[0].Height

	for _, comp := range group[1:] {
		if comp.X < minX {
			minX = comp.X
		}
		if comp.Y < minY {
			minY = comp.Y
		}
		if comp.X+comp.Width > maxX {
			maxX = comp.X + comp.Width
		}
		if comp.Y+comp.Height > maxY {
			maxY = comp.Y + comp.Height
		}
	}

	return WordBox{
		X:          minX,
		Y:          minY,
		Width:      maxX - minX,
		Height:     maxY - minY,
		Text:       fmt.Sprintf("merged_custom_%d", len(group)),
		Confidence: 85.0,
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
