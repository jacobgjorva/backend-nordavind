package motor

import (
	"strings"
	"testing"
)

// En metode skal si hva som må være SANT om svaret, ikke hvilket verktøy
// som skal kalles. Målt: instruksen «les sidene (fetch_url)» ble ignorert i
// samtlige turer, fordi web_search allerede returnerer hele sider — metoden
// ba om en dublett. En metode som navngir verktøy binder seg dessuten til
// dagens verktøyoppsett og må skrives om hver gang det endres.
func TestMethodTextsDescribeGoalsNotToolNames(t *testing.T) {
	toolNames := []string{"web_search", "fetch_url", "query_database", "show_table", "fetch_series"}
	for key, m := range Catalog {
		for _, name := range toolNames {
			if strings.Contains(m.Text, name) {
				t.Errorf("%s: metodeteksten navngir verktøyet %q — beskriv målet i stedet", key, name)
			}
		}
	}
}
