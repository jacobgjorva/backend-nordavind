package search

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestExtractDOCX(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create("word/document.xml")
	f.Write([]byte(`<?xml version="1.0"?><w:document><w:body>` +
		`<w:p><w:r><w:t>Ordrer over</w:t></w:r><w:r><w:t> 5000 kr</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>m&#229; godkjennes av leder.</w:t></w:r></w:p>` +
		`</w:body></w:document>`))
	zw.Close()

	out := ExtractDOCX(buf.Bytes(), 1000)
	if !strings.Contains(out, "Ordrer over 5000 kr") {
		t.Fatalf("mangler første avsnitt: %q", out)
	}
	if !strings.Contains(out, "må godkjennes av leder.") {
		t.Fatalf("entitet/andre avsnitt feil: %q", out)
	}
}
