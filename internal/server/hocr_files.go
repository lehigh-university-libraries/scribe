package server

// Session hOCR is persisted in the ocr_runs table. The older filesystem cache
// is intentionally disabled so API and worker instances do not depend on local
// disk for session state.

func writeSessionHOCR(_ string, _ string, _ string) error {
	return nil
}

func readSessionHOCR(_ string, _ string) (string, bool) {
	return "", false
}

func readPreferredSessionHOCR(_ string) (string, bool) {
	return "", false
}
