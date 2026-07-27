package api

import (
	"sort"
	"strings"

	"github.com/jacobgjorva/backend-nordavind/internal/search"
)

// Sosiale medier og UGC-plattformer rangeres ofte øverst av søkemotorene, men
// bærer nesten aldri etterprøvbare fakta: «Aurora Aksnes fødselsdato» ga
// YouTube/Reddit/Instagram (bursdagssangen «Hurra for deg») mens Wikipedia,
// som HAR datoen, falt ut av topp-4. Modellen fikk utdrag uten faktumet,
// søkte om igjen til rundene tok slutt, og svarte tomt.
//
// Vi rangerer derfor slike domener BAKERST før sidene kuttes til topp-N — de
// forsvinner bare hvis det finnes bedre kilder, så et YouTube-treff på et
// spørsmål som faktisk handler om en video består. Kvalitetsnøytralt og
// generelt, ikke en regel for én entitet.
var socialDomains = []string{
	"youtube.com", "youtu.be", "reddit.com", "instagram.com", "tiktok.com",
	"facebook.com", "twitter.com", "x.com", "pinterest.com", "threads.net",
	"quora.com", "tumblr.com", "snapchat.com",
}

// isSocial er sann når URL-en hører til et UGC-/sosiale-medier-domene.
func isSocial(url string) bool {
	u := strings.ToLower(url)
	for _, d := range socialDomains {
		// «//d» eller «.d» treffer domenet og subdomener, aldri en tilfeldig
		// delstreng i en sti.
		if strings.Contains(u, "//"+d) || strings.Contains(u, "//www."+d) ||
			strings.Contains(u, "."+d) {
			return true
		}
	}
	return false
}

// demoteSocial stabilsorterer resultatene slik at faktakilder beholder sin
// rekkefølge først og sosiale medier havner bakerst — uten å fjerne noe.
func demoteSocial(results []search.Result) []search.Result {
	out := append([]search.Result(nil), results...)
	sort.SliceStable(out, func(i, j int) bool {
		return !isSocial(out[i].URL) && isSocial(out[j].URL)
	})
	return out
}
