package api

import "fmt"

// Kildekontrakten som står rett foran svaret modellen skriver.
//
// Den gamle formuleringen blandet to ting: HVOR fakta kommer fra, og HVA
// modellen får lov å gjøre med dem. «Dekker ikke kildene svaret, si at du
// ikke fant pålitelig informasjon» gjorde enhver SLUTNING ulovlig — også de
// åpenbare. «Ordet kåres i desember, så 2026-ordet kommer til vinteren» er
// ikke et nytt faktum, men en følge av to fakta som står i kildene. Effekten
// var en assistent som refererte i stedet for å tenke, uavhengig av modell.
//
// Kildekravet gjelder derfor FAKTA. Å resonnere over dem er ikke bare lov,
// det er poenget. Diktede fakta stoppes fortsatt av kildekontrollen
// (grounding.go), som måler tall og navn mot denne teksten.
func sourceHeader(query string) string {
	return fmt.Sprintf("Websøk (%s). Alle FAKTA om dette temaet — tall, navn, datoer, "+
		"hendelser — skal hentes fra kildene under, aldri fra egen hukommelse. "+
		"Du skal derimot TENKE med det du finner: trekk de slutningene fakta i "+
		"kildene gir grunnlag for, og si hva de betyr for det brukeren spurte om. "+
		"Mangler et faktum i kildene, si det — men ikke hold tilbake en slutning "+
		"som følger av det som faktisk står der. Oppgi URL når du siterer:", query)
}
