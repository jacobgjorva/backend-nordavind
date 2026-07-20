package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/jacobgjorva/backend-nordavind/internal/search"
)

const (
	maxUploadBytes  = 10 << 20 // 10 MB
	maxExtractChars = 20000
)

// handleExtract tar imot en fil (multipart) og returnerer ren tekst —
// PDF via PDF-motoren, ellers tolket som tekstfil.
func (s *Server) handleExtract(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, "ugyldig opplasting", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "mangler fil", http.StatusBadRequest)
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maxUploadBytes))
	if err != nil {
		http.Error(w, "kunne ikke lese fil", http.StatusBadRequest)
		return
	}

	name := header.Filename
	var text string
	switch {
	case strings.HasSuffix(strings.ToLower(name), ".pdf") || bytes.HasPrefix(raw, []byte("%PDF")):
		text = search.ExtractPDF(raw, maxExtractChars)
	case strings.HasSuffix(strings.ToLower(name), ".docx"):
		text = search.ExtractDOCX(raw, maxExtractChars)
	case utf8.Valid(raw):
		text = string(raw)
		if runes := []rune(text); len(runes) > maxExtractChars {
			text = string(runes[:maxExtractChars])
		}
	default:
		http.Error(w, "filtypen støttes ikke (tekst, PDF og Word/.docx)", http.StatusUnsupportedMediaType)
		return
	}

	if strings.TrimSpace(text) == "" {
		http.Error(w, "fant ingen tekst i filen", http.StatusUnprocessableEntity)
		return
	}

	s.log.Info("filuttrekk", "fil", name, "tegn", len(text))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"name": name, "text": text})
}
