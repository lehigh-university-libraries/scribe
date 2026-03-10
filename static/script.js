// ============================================================================
// GLOBAL STATE VARIABLES
// ============================================================================

// Session and image state
let currentSession = null;
let currentImageIndex = 0;

// hOCR data and navigation state
let hocrData = null;
let selectedWordId = null;
let currentWordIndex = -1;
let currentLineWords = [];
let currentLineId = null;
let allLines = [];
let currentLineIndex = -1;

// Drawing mode state
let drawingMode = false;
let isDrawing = false;
let drawingStart = null;
let currentDrawingBox = null;
let pendingAnnotation = null;

// ============================================================================
// INITIALIZATION AND EVENT HANDLERS
// ============================================================================

document.addEventListener("DOMContentLoaded", function () {
  const urlParams = new URLSearchParams(window.location.search);
  const sessionParam = urlParams.get("session");

  if (sessionParam) {
    loadSession(sessionParam);
  } else {
    loadSessions();
  }
});

document.addEventListener("keydown", function (e) {
  // Only handle navigation when correction interface is visible
  if (
    document.getElementById("correction-section").classList.contains("hidden")
  ) {
    return;
  }

  switch (e.key) {
    case "Tab":
      e.preventDefault();
      if (e.shiftKey) {
        navigateToLineAbove();
      } else {
        navigateToLineBelow();
      }
      break;
    case "Enter":
      e.preventDefault();
      if (
        selectedWordId &&
        document.getElementById("word-text") === document.activeElement
      ) {
        applyWordChanges();
      }
      break;
    case "Delete":
      if (
        currentLineId &&
        document.activeElement !== document.getElementById("line-text-area")
      ) {
        e.preventDefault();
        deleteSelectedLine();
      }
      break;
    case "Escape":
      e.preventDefault();
      if (drawingMode) {
        toggleDrawingMode();
      } else {
        clearSelection();
      }
      break;
    case "d":
    case "D":
      if (
        document.activeElement === document.body ||
        document.activeElement === document.getElementById("image-container") ||
        document.activeElement.tagName === "BUTTON"
      ) {
        e.preventDefault();
        toggleDrawingMode();
      }
      break;
  }
});

// ============================================================================
// DRAWING MODE FUNCTIONS
// ============================================================================

function toggleDrawingMode() {
  drawingMode = !drawingMode;
  const btn = document.getElementById("drawing-mode-btn");
  const imageContainer = document.getElementById("image-container");

  if (drawingMode) {
    btn.textContent = "❌ Exit Drawing";
    btn.classList.remove("btn-primary");
    btn.classList.add("btn-danger");
    imageContainer.classList.add("drawing-mode");
    clearSelection();
    setupDrawingEvents();
  } else {
    btn.textContent = "✏️ Draw New Line";
    btn.classList.remove("btn-danger");
    btn.classList.add("btn-primary");
    imageContainer.classList.remove("drawing-mode");
    removeDrawingEvents();
    cancelAnnotation();
  }
}

function setupDrawingEvents() {
  const imageContainer = document.getElementById("image-container");
  imageContainer.addEventListener("mousedown", startDrawing);
  imageContainer.addEventListener("mousemove", updateDrawing);
  imageContainer.addEventListener("mouseup", endDrawing);
  imageContainer.addEventListener("mouseleave", cancelDrawing);
}

function removeDrawingEvents() {
  const imageContainer = document.getElementById("image-container");
  imageContainer.removeEventListener("mousedown", startDrawing);
  imageContainer.removeEventListener("mousemove", updateDrawing);
  imageContainer.removeEventListener("mouseup", endDrawing);
  imageContainer.removeEventListener("mouseleave", cancelDrawing);
}

function startDrawing(e) {
  if (!drawingMode) return;

  // Prevent scrolling and other default behaviors
  e.preventDefault();
  e.stopPropagation();

  isDrawing = true;
  const rect = e.currentTarget.getBoundingClientRect();
  const img = document.getElementById("current-image");
  const imgRect = img.getBoundingClientRect();

  // Calculate position relative to image
  drawingStart = {
    x: e.clientX - imgRect.left,
    y: e.clientY - imgRect.top,
  };

  // Create drawing box
  currentDrawingBox = document.createElement("div");
  currentDrawingBox.className = "drawing-box";
  currentDrawingBox.style.left = drawingStart.x + "px";
  currentDrawingBox.style.top = drawingStart.y + "px";
  currentDrawingBox.style.width = "0px";
  currentDrawingBox.style.height = "0px";

  document.getElementById("hocr-overlay").appendChild(currentDrawingBox);
}

function updateDrawing(e) {
  if (!drawingMode || !isDrawing || !currentDrawingBox) return;

  // Prevent scrolling and other default behaviors
  e.preventDefault();
  e.stopPropagation();

  const img = document.getElementById("current-image");
  const imgRect = img.getBoundingClientRect();

  const currentX = e.clientX - imgRect.left;
  const currentY = e.clientY - imgRect.top;

  const left = Math.min(drawingStart.x, currentX);
  const top = Math.min(drawingStart.y, currentY);
  const width = Math.abs(currentX - drawingStart.x);
  const height = Math.abs(currentY - drawingStart.y);

  currentDrawingBox.style.left = left + "px";
  currentDrawingBox.style.top = top + "px";
  currentDrawingBox.style.width = width + "px";
  currentDrawingBox.style.height = height + "px";
}

function endDrawing(e) {
  if (!drawingMode || !isDrawing || !currentDrawingBox) return;

  isDrawing = false;

  // Check if the drawn box is large enough
  const width = parseFloat(currentDrawingBox.style.width);
  const height = parseFloat(currentDrawingBox.style.height);

  if (width < 10 || height < 10) {
    // Too small, cancel
    cancelDrawing();
    return;
  }

  // Store the bounding box coordinates
  const img = document.getElementById("current-image");
  const scaleX = img.naturalWidth / img.clientWidth;
  const scaleY = img.naturalHeight / img.clientHeight;

  const left = parseFloat(currentDrawingBox.style.left);
  const top = parseFloat(currentDrawingBox.style.top);

  pendingAnnotation = {
    bbox: [
      Math.round(left * scaleX),
      Math.round(top * scaleY),
      Math.round((left + width) * scaleX),
      Math.round((top + height) * scaleY),
    ],
    element: currentDrawingBox,
  };

  // Show annotation modal
  showAnnotationModal();
}

function cancelDrawing() {
  if (currentDrawingBox) {
    currentDrawingBox.remove();
    currentDrawingBox = null;
  }
  isDrawing = false;
  drawingStart = null;
}

function showAnnotationModal() {
  document.getElementById("annotation-modal").classList.remove("hidden");
  const input = document.getElementById("annotation-text");
  input.value = "";
  input.focus();

  // Handle Enter key in modal
  input.onkeydown = function (e) {
    if (e.key === "Enter") {
      e.preventDefault();
      saveAnnotation();
    } else if (e.key === "Escape") {
      e.preventDefault();
      cancelAnnotation();
    }
  };
}

// Function to renumber all word IDs sequentially
function renumberWordIds() {
  if (!hocrData || !hocrData.words) return;

  // Sort words by reading order first
  hocrData.words.sort((a, b) => {
    const yDiff = a.bbox[1] - b.bbox[1];
    if (Math.abs(yDiff) > 10) {
      return yDiff;
    }
    return a.bbox[0] - b.bbox[0];
  });

  // Renumber all words sequentially
  hocrData.words.forEach((word, index) => {
    word.id = "word_" + (index + 1);
  });
}

function saveAnnotation() {
  const text = document.getElementById("annotation-text").value.trim();
  if (!text || !pendingAnnotation) {
    cancelAnnotation();
    return;
  }

  // Split the input text into individual words
  const words = text.split(/\s+/).filter((w) => w.length > 0);
  if (words.length === 0) {
    cancelAnnotation();
    return;
  }

  // Create a new line ID
  const lineId = "line_new_" + Date.now();

  // Calculate the line bbox from the drawn rectangle
  const lineBbox = pendingAnnotation.bbox; // [x1, y1, x2, y2]
  const lineWidth = lineBbox[2] - lineBbox[0];

  // Distribute words evenly along the line width
  const newWords = [];
  words.forEach((word, index) => {
    // Calculate word position along the line
    const wordWidth = lineWidth / words.length;
    const wordLeft = lineBbox[0] + index * wordWidth;
    const wordRight = wordLeft + wordWidth;

    // Create word with distributed bbox
    const newWord = {
      id: "temp_word_" + index,
      text: word,
      bbox: [
        Math.round(wordLeft),
        lineBbox[1], // Same top as line
        Math.round(wordRight),
        lineBbox[3], // Same bottom as line
      ],
      confidence: 95, // High confidence for manually added words
      line_id: lineId,
    };

    newWords.push(newWord);
  });

  // Add all words to hocrData
  if (!hocrData) {
    hocrData = { words: [] };
  }
  hocrData.words.push(...newWords);

  // Renumber all word IDs to maintain proper sequential order
  renumberWordIds();

  // Remove the drawing box since we'll recreate it properly
  if (pendingAnnotation.element) {
    pendingAnnotation.element.remove();
  }

  // Re-render the entire overlay to ensure proper indexing and event handlers
  renderHOCROverlay();

  // Update hOCR source and metrics
  updateHOCRSource();
  updateMetrics();
  updateWordCounter();

  // Close modal and reset
  closeAnnotationModal();

  // Select the first word of the newly created line
  setTimeout(() => {
    const firstNewWord = hocrData.words.find((w) => w.line_id === lineId);
    if (firstNewWord) {
      currentWordIndex = hocrData.words.findIndex(
        (w) => w.id === firstNewWord.id
      );
      selectWord(firstNewWord.id);
    }
  }, 100);
}

function cancelAnnotation() {
  if (pendingAnnotation && pendingAnnotation.element) {
    pendingAnnotation.element.remove();
  }
  pendingAnnotation = null;
  closeAnnotationModal();
  cancelDrawing();
}

function closeAnnotationModal() {
  document.getElementById("annotation-modal").classList.add("hidden");
  document.getElementById("annotation-text").onkeydown = null;
}

// ============================================================================
// NAVIGATION FUNCTIONS
// ============================================================================

function navigateToNextWord() {
  if (!hocrData || !hocrData.words || hocrData.words.length === 0) return;

  // Navigate within line first, then globally
  if (currentLineWords.length > 0 && selectedWordId) {
    const currentWordIndexInLine = currentLineWords.findIndex(
      (w) => w.id === selectedWordId
    );
    if (
      currentWordIndexInLine !== -1 &&
      currentWordIndexInLine < currentLineWords.length - 1
    ) {
      const nextWord = currentLineWords[currentWordIndexInLine + 1];
      const globalIndex = hocrData.words.findIndex((w) => w.id === nextWord.id);
      currentWordIndex = globalIndex;
      selectWord(nextWord.id);
      scrollWordIntoView();
      return;
    }
  }
  currentWordIndex = (currentWordIndex + 1) % hocrData.words.length;
  selectWordByIndex(currentWordIndex);
  scrollWordIntoView();
}

function navigateToPreviousWord() {
  if (!hocrData || !hocrData.words || hocrData.words.length === 0) return;

  // Navigate within line first, then globally
  if (currentLineWords.length > 0 && selectedWordId) {
    const currentWordIndexInLine = currentLineWords.findIndex(
      (w) => w.id === selectedWordId
    );
    if (currentWordIndexInLine > 0) {
      const prevWord = currentLineWords[currentWordIndexInLine - 1];
      const globalIndex = hocrData.words.findIndex((w) => w.id === prevWord.id);
      currentWordIndex = globalIndex;
      selectWord(prevWord.id);
      scrollWordIntoView();
      return;
    }
  }
  currentWordIndex =
    currentWordIndex <= 0 ? hocrData.words.length - 1 : currentWordIndex - 1;
  selectWordByIndex(currentWordIndex);
  scrollWordIntoView();
}

function selectWordByIndex(index) {
  if (!hocrData || !hocrData.words[index]) return;

  const word = hocrData.words[index];
  selectWord(word.id);
  updateWordCounter();

  // Auto-focus for immediate editing
  setTimeout(() => {
    const wordInput = document.getElementById("word-text");
    if (wordInput) {
      wordInput.focus();
      wordInput.select();
    }
  }, 100);
}

function navigateToLineAbove() {
  if (!allLines || allLines.length === 0 || currentLineIndex <= 0) return;

  currentLineIndex--;
  const targetLine = allLines[currentLineIndex];

  if (targetLine && targetLine.words.length > 0) {
    selectLine(targetLine.id, currentLineIndex);
    scrollLineIntoView();
  }
}

function navigateToLineBelow() {
  if (
    !allLines ||
    allLines.length === 0 ||
    currentLineIndex >= allLines.length - 1
  )
    return;

  currentLineIndex++;
  const targetLine = allLines[currentLineIndex];

  if (targetLine && targetLine.words.length > 0) {
    selectLine(targetLine.id, currentLineIndex);
    scrollLineIntoView();
  }
}

function scrollLineIntoView() {
  if (!currentLineId) return;

  const lineBox = document.getElementById("line-box-" + currentLineId);
  if (lineBox) {
    const imageContainer = document.getElementById("image-container");
    const containerRect = imageContainer.getBoundingClientRect();
    const lineRect = lineBox.getBoundingClientRect();

    if (
      lineRect.top < containerRect.top ||
      lineRect.bottom > containerRect.bottom ||
      lineRect.left < containerRect.left ||
      lineRect.right > containerRect.right
    ) {
      const scrollTop =
        imageContainer.scrollTop +
        lineRect.top -
        containerRect.top -
        containerRect.height / 2;
      const scrollLeft =
        imageContainer.scrollLeft +
        lineRect.left -
        containerRect.left -
        containerRect.width / 2;

      imageContainer.scrollTo({
        top: Math.max(0, scrollTop),
        left: Math.max(0, scrollLeft),
        behavior: "smooth",
      });
    }
  }
}

// Compatibility wrapper
function scrollWordIntoView() {
  scrollLineIntoView();
}

function updateWordCounter() {
  const counter = document.getElementById("word-counter");
  if (hocrData && hocrData.words) {
    counter.textContent = `Word ${currentWordIndex + 1} of ${
      hocrData.words.length
    }`;
  } else {
    counter.textContent = "Word 0 of 0";
  }
}

function clearSelection() {
  resetNavigationState();

  document.querySelectorAll(".hocr-line-box").forEach((box) => {
    box.classList.remove("selected", "adjacent-clickable");
  });

  const dimmingOverlay = document.getElementById("dimming-overlay");
  if (dimmingOverlay) {
    dimmingOverlay.classList.remove("active");
    dimmingOverlay.style.clipPath = "none";
  }

  document.getElementById("line-editor").style.display = "none";
  document.getElementById("no-selection").style.display = "block";
  updateWordCounter();
}

function resetNavigationState() {
  selectedWordId = null;
  currentWordIndex = -1;
  currentLineWords = [];
  currentLineId = null;
  currentLineIndex = -1;
}

function applyWordChanges() {
  if (selectedWordId) {
    updateSelectedWord();
    // Auto-advance after editing
    setTimeout(() => navigateToNextWord(), 100);
  }
}

// ============================================================================
// SESSION AND FILE MANAGEMENT
// ============================================================================

async function loadSessions() {
  try {
    const response = await fetch("api/sessions");
    const sessions = await response.json();
    displaySessions(sessions);
  } catch (error) {
    console.error("Error loading sessions:", error);
  }
}

function displaySessions(sessions) {
  const container = document.getElementById("sessions-list");
  if (sessions.length === 0) {
    container.innerHTML =
      "<p>No sessions found. Upload images to get started.</p>";
    return;
  }

  const html = sessions
    .map(
      (session) =>
        `<div style="border: 1px solid #333; padding: 15px; margin: 10px 0; border-radius: 8px; background: #111;">
        <h4>Session: ${session.id}</h4>
        <p>Images: ${session.images.length} | Completed: ${
          session.images.filter((img) => img.completed).length
        }</p>
        <p>Created: ${new Date(session.created_at).toLocaleString()}</p>
        <button class="btn btn-primary" onclick="loadSession('${
          session.id
        }')">Continue</button>
        </div>`
    )
    .join("");
  container.innerHTML = html;
}

async function handleUpload() {
  const fileInput = document.getElementById("file-input");
  const files = fileInput.files;
  if (files.length === 0) {
    alert("Please select files");
    return;
  }

  const uploadArea = document.getElementById("upload-area");
  uploadArea.innerHTML =
    "<h3>Processing files...</h3><p>Please wait while files are uploaded and processed with OCR.</p>";

  const formData = new FormData();
  for (let file of files) {
    formData.append("files", file);
  }

  try {
    const response = await fetch("api/upload", {
      method: "POST",
      body: formData,
    });

    const result = await response.json();

    if (!response.ok) {
      throw new Error(result.error || "Upload failed");
    }

    if (result.session_id) {
      console.log("Upload successful:", result.message);
      loadSession(result.session_id);
    } else {
      throw new Error("No session ID received");
    }
  } catch (error) {
    console.error("Upload error:", error);
    alert("Upload failed: " + error.message);

    resetUploadArea();
  }
}

async function handleUrlUpload() {
  const urlInput = document.getElementById("url-input");
  const imageUrl = urlInput.value.trim();

  if (!imageUrl) {
    alert("Please enter an image URL");
    return;
  }

  try {
    new URL(imageUrl);
  } catch {
    alert("Please enter a valid URL");
    return;
  }

  const uploadArea = document.getElementById("upload-area");
  uploadArea.innerHTML =
    "<h3>Processing image URL...</h3><p>Please wait while the image is downloaded and processed with OCR.</p>";

  try {
    const response = await fetch("api/upload", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        image_url: imageUrl,
      }),
    });

    const result = await response.json();

    if (!response.ok) {
      throw new Error(result.error || "URL processing failed");
    }

    if (result.session_id) {
      console.log("URL processing successful:", result.message);
      loadSession(result.session_id);
    } else {
      throw new Error("No session ID received");
    }
  } catch (error) {
    console.error("URL processing error:", error);
    alert("URL processing failed: " + error.message);
    resetUploadArea();
  }
}

function resetUploadArea() {
  const uploadArea = document.getElementById("upload-area");
  uploadArea.innerHTML = `
        <h3>Start New Scribe Session</h3>
        <p>Upload images or provide an image URL to generate and edit canonical IIIF text annotations</p>

        <!-- File Upload -->
        <div class="upload-method">
            <h4>Upload from Computer</h4>
            <input type="file" id="file-input" accept=".jpg,.jpeg,.png,.gif,.csv" multiple style="margin: 10px 0;">
            <br>
            <button class="btn btn-primary" onclick="handleUpload()">Upload & Process</button>
        </div>

        <!-- URL Input -->
        <div class="upload-method" style="margin-top: 30px; padding-top: 20px; border-top: 1px solid #333;">
            <h4>Process from Image URL</h4>
            <input type="url" id="url-input" placeholder="https://example.com/image.jpg" style="width: 100%; margin: 10px 0; padding: 8px; border: 1px solid #333; background: #111; color: #fff; border-radius: 4px;">
            <br>
            <button class="btn btn-primary" onclick="handleUrlUpload()">Process URL</button>
        </div>
    `;
}

async function saveToIslandora() {
  if (!currentSession) {
    alert("No active session");
    return;
  }

  const isDrupalSession =
    currentSession.images &&
    currentSession.images.length > 0 &&
    currentSession.images[0].drupal_upload_url;

  if (!isDrupalSession) {
    alert("This session was not created from a Drupal node");
    return;
  }

  const hocrData = getCurrentHOCR();
  if (!hocrData) {
    alert("No HOCR data to save");
    return;
  }

  const button = document.getElementById("save-islandora-btn");
  const originalText = button.textContent;
  button.textContent = "Saving...";
  button.disabled = true;

  try {
    const drupalUploadURL = currentSession.images[0].drupal_upload_url;
    const response = await fetch(drupalUploadURL, {
      method: "POST",
      headers: {
        "Content-Type": "text/vnd.hocr+html",
        "Content-Location":
          "private://derivatives/hocr/gcloud/" +
          currentSession.images[0].drupal_nid +
          ".hocr",
      },
      body: hocrData,
    });

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(`Upload failed: HTTP ${response.status} - ${errorText}`);
    }

    alert("Successfully saved to Islandora!");
    console.log("Islandora upload successful:", `HTTP ${response.status}`);
  } catch (error) {
    console.error("Islandora upload error:", error);
    alert("Failed to save to Islandora: " + error.message);
  } finally {
    button.textContent = originalText;
    button.disabled = false;
  }
}

function getCurrentHOCR() {
  if (
    !currentSession ||
    !currentSession.images ||
    currentSession.images.length === 0
  ) {
    return null;
  }

  const currentImage = currentSession.images[currentImageIndex];
  return currentImage.corrected_hocr || currentImage.original_hocr;
}

function checkForDrupalSession() {
  const button = document.getElementById("save-islandora-btn");
  if (!button) return;

  const isDrupalSession =
    currentSession &&
    currentSession.images &&
    currentSession.images.length > 0 &&
    currentSession.images[0].drupal_upload_url;

  if (isDrupalSession) {
    button.classList.remove("hidden");
  } else {
    button.classList.add("hidden");
  }
}

async function loadSession(sessionId) {
  try {
    const response = await fetch("api/sessions/" + sessionId);
    currentSession = await response.json();
    currentImageIndex = currentSession.current || 0;
    showCorrectionInterface();
    loadCurrentImage();
  } catch (error) {
    console.error("Error loading session:", error);
  }
}

function showCorrectionInterface() {
  document.getElementById("upload-section").classList.add("hidden");
  document.getElementById("correction-section").classList.remove("hidden");
  checkForDrupalSession();
}

async function loadCurrentImage() {
  if (!currentSession || currentImageIndex >= currentSession.images.length) {
    finishSession();
    return;
  }

  if (drawingMode) {
    toggleDrawingMode();
  }

  const image = currentSession.images[currentImageIndex];
  const img = document.getElementById("current-image");

  img.onload = function () {
    parseAndDisplayHOCR(image.corrected_hocr || image.original_hocr);
    updateProgress();
    updateMetrics();

    resetNavigationState();
    allLines = [];
    clearSelection();
  };

  img.src = image.image_url || "/static/uploads/" + image.image_path;
}

// ============================================================================
// hOCR PARSING AND RENDERING
// ============================================================================

async function parseAndDisplayHOCR(hocrXML) {
  try {
    const response = await fetch("api/hocr/parse", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ hocr: hocrXML }),
    });
    hocrData = await response.json();

    if (hocrData && hocrData.words) {
      renumberWordIds();
      normalizeWordData();
    }

    renderHOCROverlay();
    updateWordCounter();
  } catch (error) {
    console.error("Error parsing hOCR:", error);
  }
}

function normalizeWordData() {
  hocrData.words.forEach((word, index) => {
    normalizeBoundingBox(word);
    normalizeLineId(word, index);
    normalizeConfidence(word);
  });
}

function normalizeBoundingBox(word) {
  if (word.bbox && typeof word.bbox === "object" && !Array.isArray(word.bbox)) {
      word.bbox = [
      word.bbox.x1 || word.bbox[0] || 0,
      word.bbox.y1 || word.bbox[1] || 0,
      word.bbox.x2 || word.bbox[2] || 0,
      word.bbox.y2 || word.bbox[3] || 0,
    ];
  }

  if (!Array.isArray(word.bbox) || word.bbox.length !== 4) {
    console.error("Invalid bbox format for word:", word);
    word.bbox = [0, 0, 100, 20];
  }

  word.bbox = word.bbox.map((val) => {
    const num = parseInt(val, 10);
    return isNaN(num) ? 0 : num;
  });
}

function normalizeLineId(word, index) {
  if (!word.line_id && word.LineID) {
    word.line_id = word.LineID;
  }
  if (!word.line_id) {
    word.line_id = `line_${index + 1}`;
  }
}

function normalizeConfidence(word) {
  if (typeof word.confidence !== "number") {
    word.confidence = parseFloat(word.confidence) || 95;
  }
}

function renderHOCROverlay() {
  const overlay = document.getElementById("hocr-overlay");
  const img = document.getElementById("current-image");

  overlay.innerHTML = "";

  if (!hocrData || !hocrData.words) return;

  if (img.naturalWidth === 0) {
    img.onload = renderHOCROverlay;
    return;
  }

  const scaleX = img.clientWidth / img.naturalWidth;
  const scaleY = img.clientHeight / img.naturalHeight;

  updateLineData();
  allLines.forEach((line, lineIndex) => {
    const lineBox = document.createElement("div");
    lineBox.className = "hocr-line-box";
    lineBox.id = "line-box-" + line.id;
    lineBox.setAttribute("data-line-index", lineIndex);
    lineBox.setAttribute("data-line-id", line.id);

    const lineBBox = calculateLineBoundingBox(line.words);
    lineBox.style.left = lineBBox.x1 * scaleX + "px";
    lineBox.style.top = lineBBox.y1 * scaleY + "px";
    lineBox.style.width = (lineBBox.x2 - lineBBox.x1) * scaleX + "px";
    lineBox.style.height = (lineBBox.y2 - lineBBox.y1) * scaleY + "px";

    const avgConf = line.avgConfidence;
    if (avgConf < 60) {
      lineBox.classList.add("low-confidence");
    } else if (avgConf < 80) {
      lineBox.classList.add("medium-confidence");
    } else {
      lineBox.classList.add("high-confidence");
    }

    lineBox.title = `Line: ${line.text} (avg conf: ${avgConf}%)`;
    lineBox.onclick = () => {
      if (!drawingMode) {
        selectLine(line.id, lineIndex);
      }
    };

    overlay.appendChild(lineBox);
  });
}

function calculateLineBoundingBox(words) {
  if (!words || words.length === 0) {
    return { x1: 0, y1: 0, x2: 0, y2: 0 };
  }

  const sortedWords = [...words].sort((a, b) => a.bbox[0] - b.bbox[0]);

  const firstWord = sortedWords[0];
  const lastWord = sortedWords[sortedWords.length - 1];

  let minY = Infinity,
    maxY = -Infinity;
  words.forEach((word) => {
    const bbox = word.bbox;
    const y1 = bbox[1];
    const y2 = bbox[3];

    minY = Math.min(minY, y1);
    maxY = Math.max(maxY, y2);
  });

  return {
    x1: firstWord.bbox[0],
    y1: minY,
    x2: lastWord.bbox[2],
    y2: maxY,
  };
}

function selectLine(lineId, lineIndex) {
  // Clear previous selection and adjacent classes
  document.querySelectorAll(".hocr-line-box").forEach((box) => {
    box.classList.remove("selected", "adjacent-clickable");
  });

  // Select new line
  const lineBox = document.getElementById("line-box-" + lineId);
  if (lineBox) {
    lineBox.classList.add("selected");
    currentLineId = lineId;
    currentLineIndex = lineIndex;

    const dimmingOverlay = document.getElementById("dimming-overlay");
    if (dimmingOverlay) {
      dimmingOverlay.classList.add("active");

      requestAnimationFrame(() => {
        const imageContainer = document.getElementById("image-container");
        const containerRect = imageContainer.getBoundingClientRect();
        const lineBoxRect = lineBox.getBoundingClientRect();

        const left =
          ((lineBoxRect.left - containerRect.left) / containerRect.width) * 100;
        const top =
          ((lineBoxRect.top - containerRect.top) / containerRect.height) * 100;
        const right =
          ((lineBoxRect.right - containerRect.left) / containerRect.width) *
          100;
        const bottom =
          ((lineBoxRect.bottom - containerRect.top) / containerRect.height) *
          100;

        const padding = 2;
        const clipLeft = Math.max(0, left - padding);
        const clipTop = Math.max(0, top - padding);
        const clipRight = Math.min(100, right + padding);
        const clipBottom = Math.min(100, bottom + padding);

        const clipPath = `polygon(
                    0% 0%, 
                    0% 100%, 
                    ${clipLeft}% 100%, 
                    ${clipLeft}% ${clipTop}%, 
                    ${clipRight}% ${clipTop}%, 
                    ${clipRight}% ${clipBottom}%, 
                    ${clipLeft}% ${clipBottom}%, 
                    ${clipLeft}% 100%, 
                    100% 100%, 
                    100% 0%
                )`;

        dimmingOverlay.style.clipPath = clipPath;
      });
    }

    for (let offset = -2; offset <= 2; offset++) {
      if (offset === 0) continue;

      const adjacentIndex = lineIndex + offset;
      if (adjacentIndex >= 0 && adjacentIndex < allLines.length) {
        const adjacentLineId = allLines[adjacentIndex].id;
        const adjacentLineBox = document.getElementById(
          "line-box-" + adjacentLineId
        );
        if (adjacentLineBox) {
          adjacentLineBox.classList.add("adjacent-clickable");
        }
      }
    }

    const line = allLines.find((l) => l.id === lineId);
    if (line && line.words && line.words.length > 0) {
      const firstWord = line.words[0];
      selectedWordId = firstWord.id;
      currentWordIndex = hocrData.words.findIndex((w) => w.id === firstWord.id);

      showLineEditor(firstWord);
    }

    updateWordCounter();
  }
}

function selectWord(wordId) {
  selectedWordId = wordId;
  currentWordIndex = hocrData.words.findIndex((w) => w.id === wordId);

  const word = hocrData.words.find((w) => w.id === wordId);
  if (word) {
    const lineId = word.line_id || word.LineID;
    if (lineId) {
      const lineIndex = allLines.findIndex((l) => l.id === lineId);
      if (lineIndex !== -1) {
        selectLine(lineId, lineIndex);
      } else {
        showLineEditor(word);
      }
    } else {
      showLineEditor(word);
    }

    updateWordCounter();
  }
}

function showLineEditor(selectedWord) {
  if (!hocrData || !hocrData.words) return;

  // Update line data
  updateLineData();

  // Set current line
  currentLineId = selectedWord.line_id;
  currentLineWords = hocrData.words.filter(
    (w) => w.line_id === selectedWord.line_id
  );
  currentLineWords.sort((a, b) => a.bbox[0] - b.bbox[0]);

  // Find current line index
  currentLineIndex = allLines.findIndex(
    (line) => line.id === selectedWord.line_id
  );

  // Show line editor
  document.getElementById("line-editor").style.display = "block";

  // Update line display
  displayLineEditor(selectedWord);
}

function updateLineData() {
  if (!hocrData || !hocrData.words) return;

  // Group words by line_id
  const lineGroups = {};
  hocrData.words.forEach((word) => {
    if (!lineGroups[word.line_id]) {
      lineGroups[word.line_id] = [];
    }
    lineGroups[word.line_id].push(word);
  });

  // Create line objects with statistics
  allLines = Object.keys(lineGroups).map((lineId) => {
    const words = lineGroups[lineId].sort((a, b) => a.bbox[0] - b.bbox[0]);
    const avgY =
      words.reduce((sum, w) => sum + (w.bbox[1] + w.bbox[3]) / 2, 0) /
      words.length;
    const avgConf =
      words.reduce((sum, w) => sum + w.confidence, 0) / words.length;
    const text = words.map((w) => w.text).join(" ");

    return {
      id: lineId,
      words: words,
      avgY: avgY,
      avgConfidence: Math.round(avgConf),
      text: text,
    };
  });

  // Sort lines by Y position (top to bottom)
  allLines.sort((a, b) => a.avgY - b.avgY);
}

function displayLineEditor(selectedWord) {
  if (!currentLineWords || currentLineWords.length === 0) return;

  // Update line counter
  document.getElementById("line-counter").textContent = `Line ${
    currentLineIndex + 1
  } of ${allLines.length}`;

  // Update line text area
  const lineText = currentLineWords.map((w) => w.text).join(" ");
  document.getElementById("line-text-area").value = lineText;

  // Update word buttons
  const lineWordsElement = document.getElementById("line-words");
  lineWordsElement.innerHTML = "";

  currentLineWords.forEach((word) => {
    const button = document.createElement("button");
    button.className = "word-button";
    if (word.id === selectedWord.id) {
      button.classList.add("selected");
    }

    button.onclick = () => selectWordInLine(word.id);

    const textSpan = document.createElement("span");
    textSpan.textContent = word.text;

    const confSpan = document.createElement("span");
    confSpan.className = "word-confidence";
    if (word.confidence >= 80) {
      confSpan.classList.add("conf-high");
    } else if (word.confidence >= 60) {
      confSpan.classList.add("conf-medium");
    } else {
      confSpan.classList.add("conf-low");
    }
    confSpan.textContent = `${word.confidence}%`;

    button.appendChild(textSpan);
    button.appendChild(confSpan);
    lineWordsElement.appendChild(button);
  });

  // Update line stats
  document.getElementById("line-word-count").textContent =
    currentLineWords.length;
  const avgConf = Math.round(
    currentLineWords.reduce((sum, w) => sum + w.confidence, 0) /
      currentLineWords.length
  );
  document.getElementById("line-avg-confidence").textContent = avgConf + "%";
}

function selectWordInLine(wordId) {
  if (selectedWordId !== wordId) {
    selectWord(wordId);
  }
}

function updateLineText() {
  if (!currentLineWords || currentLineWords.length === 0) return;

  const newText = document.getElementById("line-text-area").value;
  const words = newText.trim().split(/\s+/);

  // Update existing words or create new ones
  for (let i = 0; i < Math.max(words.length, currentLineWords.length); i++) {
    if (i < words.length && i < currentLineWords.length) {
      // Update existing word
      currentLineWords[i].text = words[i];
    } else if (i < words.length) {
      // Need to create a new word - for now, just extend the last word's text
      // This is a simplified approach - in reality you'd need to handle word boundaries
      if (currentLineWords.length > 0) {
        currentLineWords[currentLineWords.length - 1].text +=
          " " + words.slice(currentLineWords.length).join(" ");
        break;
      }
    } else {
      // Remove extra words
      const wordToRemove = currentLineWords[i];
      hocrData.words = hocrData.words.filter((w) => w.id !== wordToRemove.id);
    }
  }

  // Update the global hocrData
  currentLineWords.forEach((word) => {
    const globalWord = hocrData.words.find((w) => w.id === word.id);
    if (globalWord) {
      globalWord.text = word.text;
    }
  });

  // Refresh line editor display
  const selectedWord = hocrData.words.find((w) => w.id === selectedWordId);
  if (selectedWord) {
    displayLineEditor(selectedWord);
  }

  // Update hOCR and metrics
  updateHOCRSource();
  updateMetrics();
}


function updateSelectedWord() {
  if (!selectedWordId || !hocrData) return;

  const newText = document.getElementById("word-text").value;
  const word = hocrData.words.find((w) => w.id === selectedWordId);

  if (word) {
    word.text = newText;
    updateHOCRSource();
    updateMetrics();
  }
}

function deleteSelectedLine() {
  if (!currentLineId || !hocrData) return;

  const line = allLines.find((l) => l.id === currentLineId);
  if (!line) return;

  if (confirm(`Delete this entire line (${line.words.length} words)?`)) {
    // Remove all words in the line
    const wordsToDelete = line.words.map((w) => w.id);
    hocrData.words = hocrData.words.filter(
      (w) => !wordsToDelete.includes(w.id)
    );

    // Clear selection
    resetNavigationState();

    // Hide line editor
    document.getElementById("line-editor").style.display = "none";
    document.getElementById("no-selection").style.display = "block";

    // Update everything
    updateHOCRSource();
    renderHOCROverlay();
    updateMetrics();
    updateWordCounter();

    // Clear selection visual state
    clearSelection();
  }
}

function updateHOCRSource() {
  if (!hocrData) return;

  // Generate hOCR XML from current data
  const hocr = generateHOCRXML(hocrData);

  // Update session data
  if (currentSession && currentSession.images[currentImageIndex]) {
    currentSession.images[currentImageIndex].corrected_hocr = hocr;
  }
}

function generateHOCRXML(data) {
  // Basic hOCR XML generation
  let xml = '<?xml version="1.0" encoding="UTF-8"?>\n';
  xml +=
    '<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">\n';
  xml +=
    '<html xmlns="http://www.w3.org/1999/xhtml" xml:lang="en" lang="en">\n';
  xml += "<head>\n<title></title>\n</head>\n<body>\n";

  xml +=
    '<div class="ocr_page" id="page_1" title="bbox 0 0 ' +
    (currentSession.images[currentImageIndex].image_width || 1000) +
    " " +
    (currentSession.images[currentImageIndex].image_height || 1000) +
    '">\n';

  // Sort words by reading order (top to bottom, left to right)
  const sortedWords = [...data.words].sort((a, b) => {
    const yDiff = a.bbox[1] - b.bbox[1];
    if (Math.abs(yDiff) > 10) {
      // Allow some tolerance for same line
      return yDiff;
    }
    return a.bbox[0] - b.bbox[0];
  });

  // Group words by line_id, but ensure each line has proper structure
  const lineGroups = {};
  sortedWords.forEach((word) => {
    if (!word.line_id) {
      console.error("Word missing line_id:", word);
      return;
    }
    if (!lineGroups[word.line_id]) {
      lineGroups[word.line_id] = [];
    }
    lineGroups[word.line_id].push(word);
  });

  // Generate XML for each line - ONE WORD PER LINE
  let lineCounter = 1;
  let wordCounter = 1;

  Object.keys(lineGroups).forEach((lineId) => {
    const words = lineGroups[lineId];
    if (words.length === 0) return;

    // Sort words within line by X position
    words.sort((a, b) => a.bbox[0] - b.bbox[0]);

    // For proper hOCR structure: each word should be in its own line
    words.forEach((word) => {
      // Validate bbox format
      if (!word.bbox || word.bbox.length !== 4) {
        console.error("Invalid bbox for word:", word);
        return;
      }

      const [x1, y1, x2, y2] = word.bbox;

      // Ensure all coordinates are numbers
      if (isNaN(x1) || isNaN(y1) || isNaN(x2) || isNaN(y2)) {
        console.error("NaN values in bbox:", word.bbox, word);
        return;
      }

      const currentLineId = `line_${lineCounter}`;
      const currentWordId = `word_${wordCounter}`;

      // Generate line with single word
      xml += `  <span class="ocr_line" id="${currentLineId}" title="bbox ${x1} ${y1} ${x2} ${y2}">\n`;
      xml += `    <span class="ocrx_word" id="${currentWordId}" title="bbox ${x1} ${y1} ${x2} ${y2}; x_wconf ${
        word.confidence || 95
      }">${escapeXML(word.text)}</span>\n`;
      xml += "  </span>\n";

      lineCounter++;
      wordCounter++;
    });
  });

  xml += "</div>\n</body>\n</html>";
  return xml;
}

function escapeXML(text) {
  return text
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}


function updateProgress() {
  const total = currentSession.images.length;
  const current = currentImageIndex + 1;
  const percentage = (current / total) * 100;

  document.getElementById(
    "progress-text"
  ).textContent = `Image ${current} of ${total}`;
  document.getElementById("progress-bar").style.width = percentage + "%";
}

async function updateMetrics() {
  if (!hocrData || !currentSession) return;

  const originalText = extractTextFromHOCR(
    currentSession.images[currentImageIndex].original_hocr
  );
  const correctedText = extractTextFromHOCR(generateHOCRXML(hocrData));

  try {
    const response = await fetch(
      "api/sessions/" + currentSession.id + "/metrics",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          original: originalText,
          corrected: correctedText,
        }),
      }
    );
    const metrics = await response.json();

    document.getElementById("char-similarity").textContent =
      metrics.character_similarity.toFixed(3);
    document.getElementById("word-accuracy").textContent =
      metrics.word_accuracy.toFixed(3);
    document.getElementById("word-error-rate").textContent =
      metrics.word_error_rate.toFixed(3);
    document.getElementById("total-words").textContent = hocrData.words.length;

    // Calculate confidence metrics
    const confidences = hocrData.words.map((w) => w.confidence);
    const avgConf = confidences.reduce((a, b) => a + b, 0) / confidences.length;
    const lowConfCount = confidences.filter((c) => c < 60).length;

    document.getElementById("avg-confidence").textContent =
      Math.round(avgConf) + "%";
    document.getElementById("low-conf-words").textContent = lowConfCount;
  } catch (error) {
    console.error("Error calculating metrics:", error);
  }
}

function extractTextFromHOCR(hocrXML) {
  // Simple text extraction from hOCR
  try {
    const parser = new DOMParser();
    const doc = parser.parseFromString(hocrXML, "text/html");
    const words = doc.querySelectorAll(".ocrx_word");
    return Array.from(words)
      .map((word) => word.textContent)
      .join(" ");
  } catch (error) {
    return "";
  }
}

async function saveAndNext() {
  const hocrXML = generateHOCRXML(hocrData);
  currentSession.images[currentImageIndex].corrected_hocr = hocrXML;
  currentSession.images[currentImageIndex].completed = true;

  // Save to backend
  await saveSession();

  currentImageIndex++;
  resetNavigationState();
  allLines = [];
  document.getElementById("line-editor").style.display = "none";
  document.getElementById("no-selection").style.display = "block";

  loadCurrentImage();
}

function previousImage() {
  if (currentImageIndex > 0) {
    currentImageIndex--;
    resetNavigationState();
    allLines = [];
    document.getElementById("line-editor").style.display = "none";
    document.getElementById("no-selection").style.display = "block";
    loadCurrentImage();
  }
}

async function saveSession() {
  try {
    await fetch("api/sessions/" + currentSession.id, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(currentSession),
    });
  } catch (error) {
    console.error("Error saving session:", error);
  }
}

async function finishSession() {
  await saveSession();
  alert("Session completed. Scribe edits have been saved.");
  location.reload();
}

// Handle image resize for overlay repositioning
window.addEventListener("resize", () => {
  setTimeout(renderHOCROverlay, 100);
});

// Download formatted hOCR function
async function downloadFormattedHocr() {
  if (!hocrData || !hocrData.words || hocrData.words.length === 0) {
    alert("No hOCR data available to download");
    return;
  }

  try {
    // Generate the well-formatted hOCR XML
    const hocrXML = generateHOCRXML(hocrData);

    // Copy to clipboard
    await navigator.clipboard.writeText(hocrXML);

    // Show feedback to user
    const btn = document.getElementById("download-hocr-btn");
    const originalText = btn.textContent;
    btn.textContent = "✅ Copied to Clipboard!";
    btn.style.background = "linear-gradient(135deg, #10b981, #059669)";

    // Reset button after 2 seconds
    setTimeout(() => {
      btn.textContent = originalText;
      btn.style.background = "";
    }, 2000);
  } catch (error) {
    console.error("Error copying hOCR to clipboard:", error);

    // Fallback: create download link
    try {
      const hocrXML = generateHOCRXML(hocrData);
      const blob = new Blob([hocrXML], { type: "text/xml" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `hocr_${currentSession?.id || "export"}_image_${
        currentImageIndex + 1
      }.xml`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);

      // Show download feedback
      const btn = document.getElementById("download-hocr-btn");
      const originalText = btn.textContent;
      btn.textContent = "📁 Downloaded!";
      setTimeout(() => {
        btn.textContent = originalText;
      }, 2000);
    } catch (downloadError) {
      console.error("Error downloading hOCR:", downloadError);
      alert("Unable to copy or download hOCR data");
    }
  }
}
