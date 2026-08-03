package api

import "testing"

// Målt i prod 2026-08-03: «Kan du sjekke mailen min?» endte i fallback-teksten
// fordi gaten dømte setningsstart-ord og norske bøyningsformer som diktede
// navn, og omforsøket ble validert med streng tallsjekk mot ISO-tidsstempler.
// Testene under er de reelle avvikene fra journalctl pluss mail-parafrasene.

func mailSource() []string {
	return []string{
		"- id=AAMkAGX1 | 2026-08-03T07:12:45Z | fra Petter Berg <pb@firma.no> | PB-oppdatering | kort status om leveransen",
	}
}

func TestProseSentenceStartIsNotAName(t *testing.T) {
	prose := "Jeg sjekket innboksen. Prøvde å hente flere, men fant ingen nyere."
	if off := groundingOffenders(prose, mailSource()); len(off) != 0 {
		t.Fatalf("setningsstart skal ikke dømmes som egennavn, fikk %v", off)
	}
}

func TestProseLineAndBulletStartIsNotAName(t *testing.T) {
	prose := "To ting:\n- Slettet spam i går\n- Prøvde et nytt søk\nSnittordrevolumet er uendret."
	if off := groundingOffenders(prose, mailSource()); len(off) != 0 {
		t.Fatalf("linje- og listestart skal ikke dømmes som egennavn, fikk %v", off)
	}
}

func TestProseInflectedEntityIsCovered(t *testing.T) {
	prose := "Den siste e-posten gjelder PB-oppdateringen fra Petter Berg."
	if off := groundingOffenders(prose, mailSource()); len(off) != 0 {
		t.Fatalf("bøyd form av kildens navn skal dekkes, fikk %v", off)
	}
}

func TestProseInventedMidSentenceNameStillCaught(t *testing.T) {
	off := groundingOffenders("Du fikk også en purring fra Zorbatron Industrier i går.", mailSource())
	if len(off) == 0 || !hasNameOffender(off) {
		t.Fatalf("diktet navn midt i setning skal blokkeres, fikk %v", off)
	}
	off = groundingOffenders("E-posten nevner at Fjellrevprosjektet er forsinket.", mailSource())
	if len(off) == 0 || !hasNameOffender(off) {
		t.Fatalf("diktet enkeltords-entitet midt i setning skal fanges, fikk %v", off)
	}
}

func TestProseInventedBoldEntityStillCaught(t *testing.T) {
	off := groundingOffenders("Beste produkt: **Eplemost 1L** ifølge mailen.", mailSource())
	if len(off) == 0 {
		t.Fatal("diktet fremhevet entitet slapp gjennom")
	}
}

func TestStrictAcceptsLocalTimeOfISOTimestamp(t *testing.T) {
	// 07:12:45Z er 09:12 norsk sommertid — en omskriving, ikke dikting.
	prose := "Siste e-post kom 3. august kl. 09:12 fra Petter Berg."
	if off := strictOffenders(prose, mailSource()); len(off) != 0 {
		t.Fatalf("lokal tid av kildens tidsstempel skal dekkes, fikk %v", off)
	}
}

func TestHasNumberOffender(t *testing.T) {
	if !hasNumberOffender([]string{"63 000", "85 000"}) {
		t.Fatal("tallavvik skal gi streng omforsøks-dom")
	}
	if hasNumberOffender([]string{"PB-oppdateringen", "Zorbatron Industrier"}) {
		t.Fatal("rene navneavvik skal ikke gi streng tallsjekk")
	}
}

func TestComputedNumbersAreNotNameOffenders(t *testing.T) {
	// Prod 2026-07-31: beregnede snittall skal til faktadommeren (myk sti),
	// ikke hardblokkeres som navn.
	off := groundingOffenders("Snittordrevolumet ligger på 63 000 mot 85 000 i fjor.", mailSource())
	if len(off) == 0 || hasNameOffender(off) {
		t.Fatalf("kun tallavvik ventet her, fikk %v", off)
	}
}
