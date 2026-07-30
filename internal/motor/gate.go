package motor

// Porten: hvilke turer v6 tar, og hvilke som går urørt til den gamle løypa.
//
// Regelen er streng med vilje. Utfører-flytene (widget, eksport, rutiner,
// e-post, design, paneler) har egne LEVERANSEKONTRAKTER som motoren ikke
// kjenner: en widget-blokk som må appendes, et eksportkort, en
// rutine-kvittering. Tar motoren en slik tur, forsvinner kontrakten stille
// og brukeren får prosa der det skulle stått et kort (målt i v5).
//
// v6 eier derfor kun det den faktisk kan levere: vanlig chat, data,
// research, anbefaling og samtale.

// Budget er metoderadens tak for én tur. Rene tall, satt i katalogen —
// aldri en spesialkvote i løkka.
type Budget struct {
	Searches int // web_search per tur
	Fetches  int // fetch_url per tur
	Rounds   int // modellrunder før turen må lande
	MaxChars int // mykt svarbudsjett (0 = flytens standard)
}

// Allow: har klassen råd til ett kall til av denne typen?
func (b Budget) Allow(k ToolKind, spent int) bool {
	switch k {
	case KindSearch:
		return spent < b.Searches
	case KindFetch:
		return spent < b.Fetches
	}
	return true
}

// modeFields er payload-nøklene som markerer en spesialmodus med eget
// lerret eller veiviser. Er en av dem satt, eier den modusen turen.
var modeFields = []string{
	"nordavind_widget", "nordavind_design", "nordavind_agent_setup",
	"nordavind_connector", "nordavind_agent_edit", "nordavind_plan",
}

// executorFlows er flyter med egen leveransekontrakt. De blir aldri v6.
var executorFlows = map[string]bool{
	"create_widget": true, "edit_widget": true, "export_excel": true,
	"create_routine": true, "edit_routine": true, "email": true,
	"create_presentation": true, "upload_document": true, "contract_review": true,
}

// Takes avgjør om v6 tar turen. Fail-safe: alt ukjent går til legacy.
func Takes(payload map[string]any, flowKey string) bool {
	for _, k := range modeFields {
		if v, ok := payload[k]; ok && v != nil && v != "" && v != false {
			return false
		}
	}
	return !executorFlows[flowKey]
}
