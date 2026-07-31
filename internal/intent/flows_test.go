package intent

import "testing"

// Jacobs beslutning 2026-07-31: disse flytene utløses KUN av eksplisitte
// klienthandlinger (slash-kommando, fildropp) — aldri av chatteksten.
// Testen fester det eksakte settet som data: en ny panel-flyt som glemmer
// flagget, eller et flagg som forsvinner i en refaktorering, skal feile her.
func TestExplicitOnlySetMatchesDecision(t *testing.T) {
	want := map[string]bool{
		"usage_stats": true, "connect_database": true, "connect_m365": true,
		"manage_connections": true, "manage_users": true, "impersonate_user": true,
		"employees_admin": true, "knowledge_admin": true, "upload_document": true,
		"create_presentation": true,
	}
	for key, f := range Flows {
		if f.ExplicitOnly != want[key] {
			t.Errorf("%s: ExplicitOnly=%v, beslutningen sier %v", key, f.ExplicitOnly, want[key])
		}
	}
	for key := range want {
		if _, ok := Flows[key]; !ok {
			t.Errorf("%s står i beslutningen men finnes ikke i flyt-tabellen", key)
		}
	}
}

// contract_review er fjernet (bygges på nytt senere) — den skal ikke ligge
// igjen i hverken flyt-tabellen eller registeret.
func TestContractReviewIsGone(t *testing.T) {
	if _, ok := Flows["contract_review"]; ok {
		t.Error("contract_review ligger fortsatt i flyt-tabellen")
	}
	for _, in := range Registry {
		if in.Key == "contract_review" {
			t.Error("contract_review ligger fortsatt i registeret")
		}
	}
}
