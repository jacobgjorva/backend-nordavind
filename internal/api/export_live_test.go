package api

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

// Malen skal patches med ny URL og beholde Power Query-delene byte-intakt.
func TestBuildLiveWorkbook(t *testing.T) {
	url := "https://app.nordawind.com/v1/live/abc123/data.xlsx"
	out, err := buildLiveWorkbook(url)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	var sawMashup, sawURL bool
	for _, f := range zr.File {
		if f.Name == "customXml/item1.xml" {
			sawMashup = true
		}
		if f.Name == "xl/sharedStrings.xml" {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			if !strings.Contains(string(data), url) {
				t.Fatalf("live-URL ikke patchet inn: %s", data)
			}
			if strings.Contains(string(data), "localhost") {
				t.Fatal("gammel localhost-URL står igjen")
			}
			sawURL = true
		}
	}
	if !sawMashup || !sawURL {
		t.Fatalf("mangler deler: mashup=%v url=%v", sawMashup, sawURL)
	}
}
