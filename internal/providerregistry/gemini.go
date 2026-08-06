package providerregistry

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	// HTR v0.19.4 rejects requests above this wire size before constructing
	// the HTTP request. Keep an independent bound at the compatibility seam so
	// a future caller cannot turn the tail adapter into an unbounded reader.
	maxGeminiAdapterRequestBytes int64 = 70 << 20

	// HTR v0.19.4 emits generationConfig as the final root property. Its
	// bounded, registered values fit comfortably in this tail without retaining
	// the prompt or base64 image in memory a second time.
	maxGeminiSamplingTailBytes = 4 << 10
	geminiStreamReadBytes      = 32 << 10
)

var (
	errGeminiRequestRead       = errors.New("gemini request body read failed")
	errGeminiRequestReplay     = errors.New("gemini request body replay failed")
	errGeminiRequestTooLarge   = errors.New("gemini request body exceeds adapter limit")
	errGeminiRequestLength     = errors.New("gemini request body length is invalid")
	errGeminiRequestMalformed  = errors.New("gemini request body is malformed")
	errGeminiTransportMissing  = errors.New("gemini request transport is not configured")
	geminiGenerationConfigOpen = []byte(`,"generationConfig":{`)
	geminiTemperatureOpen      = []byte(`"temperature":`)
)

// geminiModelUsesDefaultSampling identifies Gemini models that no longer
// accept caller-controlled sampling parameters. Gemini 3.x is tuned around
// its model default and rejects or degrades requests that retain the legacy
// temperature field.
func geminiModelUsesDefaultSampling(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gemini-3-") || strings.HasPrefix(model, "gemini-3.")
}

func providerModelSupportsTemperature(descriptor Provider, model string) bool {
	if !descriptor.Capabilities.Temperature {
		return false
	}
	return descriptor.ID != "gemini" || !geminiModelUsesDefaultSampling(model)
}

// geminiModelHTTPClient adapts the pinned HTR Gemini client's legacy request
// shape without changing its endpoint, authentication, bounds, content length,
// request replay semantics, or response handling. HTR v0.19.4 represents
// Temperature as a required float and therefore cannot omit it for Gemini 3.x.
func geminiModelHTTPClient(client *http.Client, model string) *http.Client {
	if !geminiModelUsesDefaultSampling(model) {
		return client
	}

	adapted := &http.Client{}
	if client != nil {
		*adapted = *client
	}
	transport := adapted.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	adapted.Transport = geminiDefaultSamplingTransport{
		next:            transport,
		maxRequestBytes: maxGeminiAdapterRequestBytes,
	}
	return adapted
}

type geminiDefaultSamplingTransport struct {
	next            http.RoundTripper
	maxRequestBytes int64
}

func (t geminiDefaultSamplingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, errGeminiRequestMalformed
	}
	if t.next == nil {
		return nil, closeGeminiRequestBody(request.Body, errGeminiTransportMissing)
	}
	if request.Body == nil || request.Method != http.MethodPost ||
		!strings.EqualFold(strings.TrimSpace(request.Header.Get("Content-Type")), "application/json") {
		return nil, closeGeminiRequestBody(request.Body, errGeminiRequestMalformed)
	}
	if request.ContentLength <= 0 {
		return nil, closeGeminiRequestBody(request.Body, errGeminiRequestLength)
	}
	if t.maxRequestBytes <= 0 {
		return nil, closeGeminiRequestBody(request.Body, errGeminiRequestMalformed)
	}
	if request.ContentLength > t.maxRequestBytes {
		return nil, closeGeminiRequestBody(request.Body, errGeminiRequestTooLarge)
	}

	adapted := request.Clone(request.Context())
	adapted.Body = newGeminiSamplingBody(request.Body, request.ContentLength, t.maxRequestBytes)
	// Clone preserves ContentLength. The transformation uses equal-length
	// whitespace so both the declared and transmitted lengths stay unchanged.
	if request.GetBody == nil {
		adapted.GetBody = nil
	} else {
		getBody := request.GetBody
		expectedLength := request.ContentLength
		maxRequestBytes := t.maxRequestBytes
		adapted.GetBody = func() (io.ReadCloser, error) {
			body, err := getBody()
			if err != nil || body == nil {
				if body != nil {
					_ = body.Close()
				}
				return nil, errGeminiRequestReplay
			}
			return newGeminiSamplingBody(body, expectedLength, maxRequestBytes), nil
		}
	}
	return t.next.RoundTrip(adapted)
}

func closeGeminiRequestBody(body io.ReadCloser, requestErr error) error {
	if body != nil {
		if err := body.Close(); err != nil {
			return errGeminiRequestRead
		}
	}
	return requestErr
}

// geminiSamplingBody streams the request while retaining only its final
// bounded tail. Bytes that cannot be part of generationConfig are released to
// the next transport immediately. At EOF the trusted HTR suffix is verified
// and only its temperature member is replaced in place.
type geminiSamplingBody struct {
	source          io.ReadCloser
	expectedLength  int64
	maxRequestBytes int64
	bytesRead       int64
	buffer          []byte
	start           int
	end             int
	finalized       bool
	closed          bool
	terminalErr     error
	emptyReads      int
}

func newGeminiSamplingBody(source io.ReadCloser, expectedLength, maxRequestBytes int64) *geminiSamplingBody {
	return &geminiSamplingBody{
		source:          source,
		expectedLength:  expectedLength,
		maxRequestBytes: maxRequestBytes,
		buffer:          make([]byte, maxGeminiSamplingTailBytes+geminiStreamReadBytes),
	}
}

func (b *geminiSamplingBody) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if b.terminalErr != nil {
		return 0, b.terminalErr
	}

	for {
		buffered := b.end - b.start
		if b.finalized {
			if buffered == 0 {
				return 0, io.EOF
			}
			n := copy(destination, b.buffer[b.start:b.end])
			b.start += n
			return n, nil
		}
		if buffered > maxGeminiSamplingTailBytes {
			n := copy(destination, b.buffer[b.start:b.end-maxGeminiSamplingTailBytes])
			b.start += n
			return n, nil
		}

		// Compact at most the small retained tail before the next fixed-size
		// source read. Large prompt and image bytes are never copied here.
		if b.start > 0 {
			copy(b.buffer, b.buffer[b.start:b.end])
			b.end -= b.start
			b.start = 0
		}
		n, readErr := b.source.Read(b.buffer[b.end:])
		if n > 0 {
			b.emptyReads = 0
			b.end += n
			b.bytesRead += int64(n)
			if b.bytesRead > b.maxRequestBytes {
				return b.fail(errGeminiRequestTooLarge)
			}
			if b.bytesRead > b.expectedLength {
				return b.fail(errGeminiRequestLength)
			}
		} else if readErr == nil {
			b.emptyReads++
			if b.emptyReads >= 100 {
				return b.fail(errGeminiRequestRead)
			}
		}

		switch {
		case readErr == nil:
			continue
		case errors.Is(readErr, io.EOF):
			if b.bytesRead != b.expectedLength {
				return b.fail(errGeminiRequestLength)
			}
			tailStart := b.start
			if retained := b.end - tailStart; retained > maxGeminiSamplingTailBytes {
				tailStart = b.end - maxGeminiSamplingTailBytes
			}
			if err := omitGeminiDeprecatedSampling(b.buffer[tailStart:b.end]); err != nil {
				return b.fail(err)
			}
			b.finalized = true
		default:
			return b.fail(errGeminiRequestRead)
		}
	}
}

func (b *geminiSamplingBody) fail(err error) (int, error) {
	b.terminalErr = err
	return 0, err
}

func (b *geminiSamplingBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	if b.source == nil {
		return nil
	}
	if err := b.source.Close(); err != nil {
		return errGeminiRequestRead
	}
	return nil
}

// omitGeminiDeprecatedSampling recognizes only the byte-exact trailing shapes
// emitted by the pinned HTR v0.19.4 Gemini client. Equal-length whitespace
// replaces the temperature member (and its comma when another member follows),
// leaving every other byte and Content-Length untouched.
func omitGeminiDeprecatedSampling(tail []byte) error {
	configurationAt := bytes.LastIndex(tail, geminiGenerationConfigOpen)
	if configurationAt < 0 {
		return errGeminiRequestMalformed
	}
	memberStart := configurationAt + len(geminiGenerationConfigOpen)
	if !bytes.HasPrefix(tail[memberStart:], geminiTemperatureOpen) {
		return errGeminiRequestMalformed
	}
	valueStart := memberStart + len(geminiTemperatureOpen)
	valueEnd, ok := consumeJSONNumber(tail, valueStart)
	if !ok {
		return errGeminiRequestMalformed
	}

	replaceEnd := valueEnd
	remainder := tail[valueEnd:]
	switch {
	case bytes.Equal(remainder, []byte(`}}`)):
	case len(remainder) > 0 && remainder[0] == ',' && validPinnedGeminiGenerationConfig(remainder[1:]):
		replaceEnd++ // remove the separator before the preserved next member
	default:
		return errGeminiRequestMalformed
	}
	for index := memberStart; index < replaceEnd; index++ {
		tail[index] = ' '
	}
	return nil
}

func consumeJSONNumber(value []byte, start int) (int, bool) {
	if start >= len(value) {
		return 0, false
	}
	index := start
	if value[index] == '-' {
		index++
		if index >= len(value) {
			return 0, false
		}
	}
	if value[index] == '0' {
		index++
	} else {
		if value[index] < '1' || value[index] > '9' {
			return 0, false
		}
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
	}
	if index < len(value) && value[index] == '.' {
		index++
		fractionStart := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == fractionStart {
			return 0, false
		}
	}
	if index < len(value) && (value[index] == 'e' || value[index] == 'E') {
		index++
		if index < len(value) && (value[index] == '+' || value[index] == '-') {
			index++
		}
		exponentStart := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		if index == exponentStart {
			return 0, false
		}
	}
	return index, true
}

func validPinnedGeminiGenerationConfig(remainder []byte) bool {
	const thinking = `"thinkingConfig":{"includeThoughts":true}}}`
	if bytes.Equal(remainder, []byte(thinking)) {
		return true
	}
	for _, resolution := range [...]string{
		"MEDIA_RESOLUTION_UNSPECIFIED",
		"MEDIA_RESOLUTION_HIGH",
		"MEDIA_RESOLUTION_MEDIUM",
		"MEDIA_RESOLUTION_LOW",
	} {
		media := `"mediaResolution":"` + resolution + `"}}`
		if bytes.Equal(remainder, []byte(media)) {
			return true
		}
		mediaAndThinking := `"mediaResolution":"` + resolution + `",` + thinking
		if bytes.Equal(remainder, []byte(mediaAndThinking)) {
			return true
		}
	}
	return false
}
