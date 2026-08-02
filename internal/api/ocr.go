package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// Bilde-OCR (2026-08-02). Målt: chat-vision leser tall riktig men STOKKER
// tabellstruktur (rusmiddelstatistikken — gruppene byttet rader, «alkohol-
// holdig» ble «alkoholfritt»). Mistral OCR leverte samme tabell perfekt.
// Derfor: hvert opplastet bilde OCR-es deterministisk i koden, og tekst-
// innholdet følger bildet inn til modellen — den leser struktur som TEKST
// i stedet for å gjette fra piksler. Kun Mistral-API-er (Jacobs krav).

const (
	ocrModel    = "mistral-ocr-latest"
	ocrMinChars = 40 // under dette er bildet ikke et dokument — ingen støy
	ocrCacheTTL = 30 * time.Minute
)

type ocrEntry struct {
	text string
	at   time.Time
}

var (
	ocrMu    sync.Mutex
	ocrCache = map[string]ocrEntry{}
)

// ocrImage henter tekstinnholdet i et bilde via Mistral OCR, cachet på
// innholds-hash så historikk-gjensendinger aldri betaler på nytt.
func (s *Server) ocrImage(ctx context.Context, dataURL string) string {
	sum := sha256.Sum256([]byte(dataURL))
	key := hex.EncodeToString(sum[:])
	ocrMu.Lock()
	if e, ok := ocrCache[key]; ok && time.Since(e.at) < ocrCacheTTL {
		ocrMu.Unlock()
		return e.text
	}
	ocrMu.Unlock()

	payload := map[string]any{
		"model":    ocrModel,
		"document": map[string]any{"type": "image_url", "image_url": dataURL},
	}
	body, _ := json.Marshal(payload)
	req, err := s.newUpstreamPath(ctx, "/ocr", bytes.NewReader(body))
	if err != nil {
		return ""
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warn("ocr feilet", "err", err)
		return ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	text := ""
	if resp.StatusCode == 200 {
		var out struct {
			Pages []struct {
				Markdown string `json:"markdown"`
			} `json:"pages"`
		}
		if json.Unmarshal(raw, &out) == nil {
			var b strings.Builder
			for _, p := range out.Pages {
				b.WriteString(p.Markdown)
				b.WriteString("\n")
			}
			text = strings.TrimSpace(b.String())
		}
	} else {
		s.log.Warn("ocr feilet", "status", resp.StatusCode)
	}
	if len([]rune(text)) < ocrMinChars {
		text = "" // fotografier o.l. — vision alene er riktig der
	}
	ocrMu.Lock()
	ocrCache[key] = ocrEntry{text: text, at: time.Now()}
	if len(ocrCache) > 200 {
		ocrCache = map[string]ocrEntry{key: {text: text, at: time.Now()}}
	}
	ocrMu.Unlock()
	if text != "" {
		s.log.Info("bilde-ocr", "tegn", len(text))
	}
	return text
}

// enrichImagesWithOCR legger OCR-teksten som egen tekstdel RETT ETTER hvert
// bilde i meldingene. Modellen får både pikslene og den strukturerte
// teksten; tabeller leses fra teksten.
func (s *Server) enrichImagesWithOCR(ctx context.Context, full map[string]any) {
	msgs, _ := full["messages"].([]any)
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := mm["content"].([]any)
		if !ok {
			continue
		}
		var enriched []any
		changed := false
		for _, p := range parts {
			enriched = append(enriched, p)
			pm, ok := p.(map[string]any)
			if !ok || pm["type"] != "image_url" {
				continue
			}
			url, ok := pm["image_url"].(string)
			if !ok {
				if obj, isObj := pm["image_url"].(map[string]any); isObj {
					url, _ = obj["url"].(string)
				}
			}
			if url == "" || pm["nordavind_ocr"] == true {
				continue
			}
			if text := s.ocrImage(ctx, url); text != "" {
				pm["nordavind_ocr"] = true
				enriched = append(enriched, map[string]any{
					"type": "text",
					"text": "[Tekstinnhold lest fra bildet over (OCR) — bruk denne for tall og tabeller:]\n" + text,
				})
				changed = true
			}
		}
		if changed {
			mm["content"] = enriched
		}
	}
}
