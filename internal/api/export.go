package api

import (
	"archive/zip"
	"regexp"
	_ "embed"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jacobgjorva/backend-nordavind/internal/connector"
	"github.com/jacobgjorva/backend-nordavind/internal/store"
	"github.com/xuri/excelize/v2"
)

// Excel-eksport: gjør en ferdigbygget tabell fra chatten til en ren .xlsx —
// typede kolonner (tall som tall), fet overskrift, autofilter, fryst topprad
// og fornuftige kolonnebredder. Ikke en CSV-dump.

const maxExportRows = 50000
const maxExportCols = 100

// parseNumber tolker en celle som tall (norsk og engelsk format), ellers false.
// «12 345,67», «12345.67» og «-3 %» normaliseres; rene tall beholdes eksakt.
func parseNumber(s string) (float64, bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, false
	}
	// Tusenskille (vanlig og hardt mellomrom) fjernes, komma-desimal → punktum.
	t = strings.NewReplacer(" ", "", " ", "", " ", "").Replace(t)
	if strings.Count(t, ",") == 1 && strings.Count(t, ".") == 0 {
		t = strings.Replace(t, ",", ".", 1)
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// buildXLSX lager arbeidsboka fra kolonner + rader. Kolonner der alle ikke-tomme
// celler er tall skrives som tall med norsk-vennlig format.
func buildXLSX(title string, columns []string, rows [][]string) (*excelize.File, error) {
	f := excelize.NewFile()
	const sheet = "Ark1"
	f.SetSheetName(f.GetSheetName(0), sheet)

	headerStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"EFEFEF"}},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})
	if err != nil {
		return nil, err
	}
	// Tall med tusenskille; desimaler kun når de finnes.
	numStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: strPtr("#,##0.##")})
	if err != nil {
		return nil, err
	}

	// Kolonnetype: tall hvis ALLE ikke-tomme celler parser som tall.
	numeric := make([]bool, len(columns))
	for j := range columns {
		numeric[j] = false
		seen := false
		ok := true
		for _, r := range rows {
			if j >= len(r) || strings.TrimSpace(r[j]) == "" {
				continue
			}
			seen = true
			if _, isNum := parseNumber(r[j]); !isNum {
				ok = false
				break
			}
		}
		numeric[j] = seen && ok
	}

	// Overskrifter.
	for j, c := range columns {
		cell, _ := excelize.CoordinatesToCellName(j+1, 1)
		f.SetCellValue(sheet, cell, c)
	}
	endHeader, _ := excelize.CoordinatesToCellName(len(columns), 1)
	f.SetCellStyle(sheet, "A1", endHeader, headerStyle)

	// Rader.
	for i, r := range rows {
		for j := range columns {
			cell, _ := excelize.CoordinatesToCellName(j+1, i+2)
			var val string
			if j < len(r) {
				val = r[j]
			}
			if numeric[j] {
				if n, ok := parseNumber(val); ok {
					f.SetCellValue(sheet, cell, n)
					continue
				}
				f.SetCellValue(sheet, cell, val)
			} else {
				f.SetCellValue(sheet, cell, val)
			}
		}
	}

	// Tallformat på numeriske kolonner.
	for j, isNum := range numeric {
		if !isNum || len(rows) == 0 {
			continue
		}
		start, _ := excelize.CoordinatesToCellName(j+1, 2)
		end, _ := excelize.CoordinatesToCellName(j+1, len(rows)+1)
		f.SetCellStyle(sheet, start, end, numStyle)
	}

	// Kolonnebredde etter innhold (begrenset), autofilter og fryst topprad.
	for j, c := range columns {
		w := len([]rune(c))
		for _, r := range rows {
			if j < len(r) && len([]rune(r[j])) > w {
				w = len([]rune(r[j]))
			}
		}
		if w < 8 {
			w = 8
		}
		if w > 48 {
			w = 48
		}
		col, _ := excelize.ColumnNumberToName(j + 1)
		f.SetColWidth(sheet, col, col, float64(w)+2)
	}
	if len(rows) > 0 {
		endCell, _ := excelize.CoordinatesToCellName(len(columns), len(rows)+1)
		f.AutoFilter(sheet, "A1:"+endCell, nil)
	}
	f.SetPanes(sheet, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})

	if strings.TrimSpace(title) != "" {
		f.SetDocProps(&excelize.DocProperties{Title: title, Creator: "Nordavind"})
	}
	return f, nil
}

func strPtr(s string) *string { return &s }

// safeFilename gjør en tittel til et trygt .xlsx-filnavn.
func safeFilename(title string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		t = "tabell"
	}
	var b strings.Builder
	for _, r := range t {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == 'æ', r == 'ø', r == 'å', r == 'Æ', r == 'Ø', r == 'Å',
			r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" {
		name = "tabell"
	}
	if r := []rune(name); len(r) > 60 {
		name = string(r[:60])
	}
	return strings.ReplaceAll(name, " ", "-") + ".xlsx"
}

// addLiveSheet legger et «Live»-ark i boka med den personlige lenken og en kort
// oppskrift, så koblingen kan settes opp i Excel på sekunder (Data → Fra nettet).
func addLiveSheet(f *excelize.File, liveURL string) error {
	const sheet = "Live"
	if _, err := f.NewSheet(sheet); err != nil {
		return err
	}
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	f.SetCellValue(sheet, "A1", "Live-kobling til Nordavind")
	f.SetCellStyle(sheet, "A1", "A1", bold)
	f.SetCellValue(sheet, "A3", "Denne lenken henter alltid ferske tall for akkurat denne tabellen:")
	f.SetCellValue(sheet, "A4", liveURL)
	f.SetCellValue(sheet, "A6", "Slik kobler du til i Excel:")
	f.SetCellValue(sheet, "A7", "1. Data → Hent data → Fra nettet, og lim inn lenken over.")
	f.SetCellValue(sheet, "A8", "2. Velg «Ark1» og trykk Last inn. Deretter gir Oppdater alltid ferske tall.")
	f.SetCellValue(sheet, "A10", "Sikkerhet: lenken gir kun lesetilgang til dette ene datasettet og kan trekkes tilbake i Nordavind.")
	f.SetColWidth(sheet, "A", "A", 110)
	return nil
}

// runStoredQuery kjører en lagret, skrivebeskyttet spørring mot riktig
// tilkobling — samme sikkerhetssti som widget-data (SafeQuery, tenant-låst).
func (s *Server) runStoredQuery(ctx context.Context, tenantID, userID, connID, sqlText string) ([]string, [][]string, error) {
	dbCtx := s.buildDBTool(tenantID, userID, connID)
	if dbCtx == nil {
		return nil, nil, fmt.Errorf("databasen er ikke tilgjengelig")
	}
	dc, _, ok, _ := dbCtx.resolveConn(connID, sqlText)
	if !ok {
		return nil, nil, fmt.Errorf("ukjent tilkobling")
	}
	db, err := connector.Open(ctx, dc.creds)
	if err != nil {
		return nil, nil, fmt.Errorf("kunne ikke koble til databasen")
	}
	defer db.Close()
	return connector.SafeQueryN(ctx, db, dc.creds.Driver, sqlText, dc.allowed, maxExportRows)
}

// handleCreateLiveExport lager en live Excel-eksport: kjører spørringen én gang
// (validerer tilgang + gir ferskt øyeblikksbilde), utsteder et engangs-token
// (lagres kun som hash), og returnerer .xlsx med data + Live-ark. Lenken kan
// KUN kjøre denne ene lagrede spørringen — aldri vilkårlig SQL.
func (s *Server) handleCreateLiveExport(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Title        string `json:"title"`
		ConnectionID string `json:"connection_id"`
		SQL          string `json:"sql"`
		Format       string `json:"format"` // "iqy" (standard) | "xlsx"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.SQL) == "" {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}

	cols, rows, err := s.runStoredQuery(r.Context(), user.TenantID, user.ID, req.ConnectionID, req.SQL)
	if err != nil {
		http.Error(w, "spørringen feilet: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(rows) > maxExportRows {
		rows = rows[:maxExportRows]
	}

	// Token: 32 tilfeldige byte, vises aldri igjen; kun SHA-256-hashen lagres.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	token := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	if _, err := s.store.CreateExportLink(
		user.TenantID, user.ID, strings.TrimSpace(req.Title), req.ConnectionID,
		strings.TrimSpace(req.SQL), hex.EncodeToString(hash[:]),
	); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	base := strings.TrimSuffix(s.cfg.PublicBaseURL, "/") + "/v1/live/" + token

	// Standard: .iqy — én fil, dobbeltklikk, Excel åpner med live data og
	// «Oppdater» virker. Null manuelt oppsett.
	if req.Format != "xlsx" {
		iqy := "WEB\r\n1\r\n" + base + "/table\r\n\r\n" +
			"Selection=AllTables\r\n" +
			"Formatting=None\r\n" +
			"PreFormattedTextToColumns=True\r\n" +
			"ConsecutiveDelimitersAsOne=True\r\n" +
			"SingleBlockTextImport=False\r\n" +
			"DisableDateRecognition=False\r\n" +
			"DisableRedirections=False\r\n"
		name := strings.TrimSuffix(safeFilename(req.Title), ".xlsx") + ".iqy"
		w.Header().Set("Content-Type", "text/x-ms-iqy; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("X-Live-Url", base+"/data.xlsx")
		w.Write([]byte(iqy))
		return
	}

	// Alternativ: .xlsx med øyeblikksbilde + Live-ark med lenken.
	liveURL := base + "/data.xlsx"
	f, err := buildXLSX(req.Title, cols, rows)
	if err != nil {
		http.Error(w, "kunne ikke bygge filen", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if err := addLiveSheet(f, liveURL); err != nil {
		http.Error(w, "kunne ikke bygge filen", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(req.Title)+`"`)
	w.Header().Set("X-Live-Url", liveURL)
	if err := f.Write(w); err != nil {
		s.log.Error("xlsx-skriving feilet", "err", err)
	}
}

// htmlEscape er minimal escaping for celleverdier i live-HTML-tabellen.
var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// handleLiveHTML serverer ferske data som en minimal HTML-tabell — formatet
// Excels webspørringer (.iqy) leser direkte. Samme token-sikkerhet som xlsx.
func (s *Server) handleLiveHTML(w http.ResponseWriter, r *http.Request) {
	link, ok := s.liveLink(w, r)
	if !ok {
		return
	}
	cols, rows, err := s.runStoredQuery(r.Context(), link.TenantID, link.UserID, link.ConnectionID, link.SQL)
	if err != nil {
		s.log.Warn("live-eksport feilet", "id", link.ID, "err", err)
		http.Error(w, "datakilden er ikke tilgjengelig", http.StatusBadGateway)
		return
	}
	if len(rows) > maxExportRows {
		rows = rows[:maxExportRows]
	}
	s.store.TouchExportLink(link.ID)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><body><table><tr>")
	for _, c := range cols {
		b.WriteString("<th>" + htmlEscaper.Replace(c) + "</th>")
	}
	b.WriteString("</tr>")
	for _, row := range rows {
		b.WriteString("<tr>")
		for _, cell := range row {
			b.WriteString("<td>" + htmlEscaper.Replace(cell) + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table></body></html>")
	w.Write([]byte(b.String()))
}

// liveLink løser token fra path til en aktiv live-lenke (404 ellers).
func (s *Server) liveLink(w http.ResponseWriter, r *http.Request) (store.ExportLink, bool) {
	token := r.PathValue("token")
	if len(token) != 64 {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return store.ExportLink{}, false
	}
	hash := sha256.Sum256([]byte(token))
	link, err := s.store.ExportLinkByTokenHash(hex.EncodeToString(hash[:]))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return store.ExportLink{}, false
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return store.ExportLink{}, false
	}
	return link, true
}

// handleLiveXLSX serverer ferske data for en live-lenke. Ingen sesjon — tokenet
// ER tilgangen, og det låser til nøyaktig én lagret spørring (read-only).
func (s *Server) handleLiveXLSX(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if len(token) != 64 {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	hash := sha256.Sum256([]byte(token))
	link, err := s.store.ExportLinkByTokenHash(hex.EncodeToString(hash[:]))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}

	cols, rows, err := s.runStoredQuery(r.Context(), link.TenantID, link.UserID, link.ConnectionID, link.SQL)
	if err != nil {
		s.log.Warn("live-eksport feilet", "id", link.ID, "err", err)
		http.Error(w, "datakilden er ikke tilgjengelig", http.StatusBadGateway)
		return
	}
	if len(rows) > maxExportRows {
		rows = rows[:maxExportRows]
	}
	f, err := buildXLSX(link.Title, cols, rows)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	s.store.TouchExportLink(link.ID)

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Cache-Control", "no-store")
	if err := f.Write(w); err != nil {
		s.log.Error("live xlsx-skriving feilet", "err", err)
	}
}

//go:embed templates/livemal.xlsx
var liveTemplate []byte

// buildLiveWorkbook kopierer Excel-malen byte-for-byte og bytter KUN live-
// URL-en i sharedStrings (Config!A1, navngitt «LiveURL»). Malen er laget i
// ekte Excel med innebygd Power Query (DataMashup kan ikke genereres av kode),
// og M-koden leser cella — derfor holder det å patche én streng.
func buildLiveWorkbook(liveURL string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(liveTemplate), int64(len(liveTemplate)))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	urlRe := regexp.MustCompile(`<si><t>http[^<]*</t></si>`)
	for _, f := range zr.File {
		// Skjul tomme Ark1 og Config — brukeren skal lande rett i Spørring.
		if f.Name == "xl/workbook.xml" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			out := string(data)
			out = strings.Replace(out, `<sheet name="Ark1" sheetId="1" r:id="rId1"/>`, `<sheet name="Ark1" sheetId="1" state="hidden" r:id="rId1"/>`, 1)
			out = strings.Replace(out, `<sheet name="Config" sheetId="2" r:id="rId3"/>`, `<sheet name="Config" sheetId="2" state="hidden" r:id="rId3"/>`, 1)
			w, err := zw.Create(f.Name)
			if err != nil {
				return nil, err
			}
			if _, err := w.Write([]byte(out)); err != nil {
				return nil, err
			}
			continue
		}
		if f.Name == "xl/sharedStrings.xml" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
			esc := strings.ReplaceAll(liveURL, "&", "&amp;")
			patched := urlRe.ReplaceAll(data, []byte("<si><t>"+esc+"</t></si>"))
			w, err := zw.Create(f.Name)
			if err != nil {
				return nil, err
			}
			if _, err := w.Write(patched); err != nil {
				return nil, err
			}
			continue
		}
		// Alle andre deler kopieres rått (bevarer DataMashup eksakt).
		raw, err := f.OpenRaw()
		if err != nil {
			return nil, err
		}
		hdr := f.FileHeader
		w, err := zw.CreateRaw(&hdr)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(w, raw); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// graphUploadXLSX laster opp/erstatter arbeidsboka i brukerens OneDrive under
// /Nordavind/. Returnerer drive-item-id og webUrl.
func (s *Server) graphUploadXLSX(ctx context.Context, accessToken, filename string, xlsx []byte) (string, string, error) {
	u := "https://graph.microsoft.com/v1.0/me/drive/root:/Nordavind/" + url.PathEscape(filename) + ":/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(xlsx))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("graph-opplasting feilet (%d): %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}
	var item struct {
		ID     string `json:"id"`
		WebURL string `json:"webUrl"`
	}
	if err := json.Unmarshal(body, &item); err != nil {
		return "", "", err
	}
	return item.ID, item.WebURL, nil
}

// xlsxBytes bygger arbeidsboka som bytes.
func xlsxBytes(title string, cols []string, rows [][]string) ([]byte, error) {
	f, err := buildXLSX(title, cols, rows)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// handleExportOneDrive oppretter en live arbeidsbok i brukerens OneDrive:
// kjører spørringen, laster opp .xlsx via Graph, og registrerer eksporten så
// push-syklusen holder fila fersk. Returnerer webUrl.
func (s *Server) handleExportOneDrive(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	var req struct {
		Title        string `json:"title"`
		ConnectionID string `json:"connection_id"`
		SQL          string `json:"sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.SQL) == "" {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}

	token, err := s.msAccessToken(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "koble til Microsoft 365 i Connectors først", http.StatusPreconditionFailed)
		return
	}
	// Valider tilgang + spørring én gang før lenken utstedes.
	if _, _, err := s.runStoredQuery(r.Context(), user.TenantID, user.ID, req.ConnectionID, req.SQL); err != nil {
		http.Error(w, "spørringen feilet: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Token-sikret live-lenke (kun hash lagres) — Power Query i malen
	// henter ferske data herfra ved hver oppdatering.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	linkToken := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(linkToken))
	if _, err := s.store.CreateExportLink(
		user.TenantID, user.ID, strings.TrimSpace(req.Title), req.ConnectionID,
		strings.TrimSpace(req.SQL), hex.EncodeToString(hash[:]),
	); err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	liveURL := strings.TrimSuffix(s.cfg.PublicBaseURL, "/") + "/v1/live/" + linkToken + "/data.xlsx"
	data, err := buildLiveWorkbook(liveURL)
	if err != nil {
		http.Error(w, "kunne ikke bygge filen", http.StatusInternalServerError)
		return
	}
	itemID, webURL, err := s.graphUploadXLSX(r.Context(), token, safeFilename(req.Title), data)
	if err != nil && strings.Contains(err.Error(), "resourceLocked") {
		// Fila med samme navn står åpen i Excel (lås) — last opp under nytt
		// navn med tidsstempel i stedet for å feile.
		alt := strings.TrimSuffix(safeFilename(req.Title), ".xlsx") +
			" " + time.Now().Format("2006-01-02 1504") + ".xlsx"
		itemID, webURL, err = s.graphUploadXLSX(r.Context(), token, alt, data)
	}
	if err != nil {
		s.log.Warn("onedrive-opplasting feilet", "err", err)
		http.Error(w, "kunne ikke laste opp til OneDrive", http.StatusBadGateway)
		return
	}
	if _, err := s.store.CreateOneDriveExport(store.OneDriveExport{
		TenantID: user.TenantID, UserID: user.ID,
		Title: strings.TrimSpace(req.Title), ConnectionID: req.ConnectionID,
		SQL: strings.TrimSpace(req.SQL), DriveItemID: itemID, WebURL: webURL,
	}); err != nil {
		s.log.Error("kunne ikke lagre onedrive-eksport", "err", err)
	}
	s.log.Info("onedrive-eksport opprettet", "user", user.ID, "item", itemID)
	writeJSON(w, map[string]string{"url": webURL})
}

// handleListExportLinks viser brukerens live-lenker (til innsyn/tilbakekalling).
func (s *Server) handleListExportLinks(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	links, err := s.store.ListExportLinks(user.ID)
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	if links == nil {
		links = []store.ExportLink{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"links": links})
}

// handleRevokeExportLink trekker tilbake en live-lenke.
func (s *Server) handleRevokeExportLink(w http.ResponseWriter, r *http.Request) {
	user, ok := s.user(w, r)
	if !ok {
		return
	}
	err := s.store.RevokeExportLink(r.PathValue("id"), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "ikke funnet", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "intern feil", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleExportXLSX tar en ferdig tabell (kolonner + rader) og returnerer en
// ren .xlsx. Statisk eksport — live kobling håndteres separat.
func (s *Server) handleExportXLSX(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.user(w, r); !ok {
		return
	}
	var req struct {
		Title   string     `json:"title"`
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "ugyldig request", http.StatusBadRequest)
		return
	}
	if len(req.Columns) == 0 || len(req.Columns) > maxExportCols {
		http.Error(w, "ugyldig kolonneantall", http.StatusBadRequest)
		return
	}
	if len(req.Rows) > maxExportRows {
		http.Error(w, fmt.Sprintf("maks %d rader", maxExportRows), http.StatusBadRequest)
		return
	}

	f, err := buildXLSX(req.Title, req.Columns, req.Rows)
	if err != nil {
		s.log.Error("xlsx-bygging feilet", "err", err)
		http.Error(w, "kunne ikke bygge filen", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeFilename(req.Title)+`"`)
	if err := f.Write(w); err != nil {
		s.log.Error("xlsx-skriving feilet", "err", err)
	}
}
