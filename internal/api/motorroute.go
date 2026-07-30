package api

import "github.com/jacobgjorva/backend-nordavind/internal/motor"

// Rutingen til metode: ett oppslag, ingen ny tilstand.
//
// HVORFOR DETTE ER SÅ LITE. Flyten er allerede avgjort semantisk av
// intent-motoren og lagt på payloaden (flowKeyField). Metoden er en ren
// funksjon av flyten (motor.For), så v6 trenger verken egen klassifisering,
// egen cache eller egen klebrighet:
//
//	Sticky arves GRATIS. En kort oppfølging («og i Sverige?») arver forrige
//	flyt via applyIntent, og siden metoden avledes av flyten, arves den med.
//	Ingen ekstra felt kan komme i utakt med rutingen.
//
// Bommer ruteren, får turen MethodNone og kjører naken løkke — dagens
// oppførsel. En feilrutet tur skal aldri arve en fremmed fremgangsmåte
// bare fordi den lignet.

// motorMethod gir metodeklassen for turen.
func motorMethod(full map[string]any) motor.MethodKey {
	key, _ := full[flowKeyField].(string)
	return motor.For(key)
}

// motorFlowKey gir flyten turen ble rutet til (til logg og porten).
func motorFlowKey(full map[string]any) string {
	key, _ := full[flowKeyField].(string)
	return key
}

// motorHasTools: har turen verktøy å arbeide med? Tenk-regelen gir bare
// mening foran en handling — uten verktøy ville den bedt om et arbeidsnotat
// til en jobb modellen ikke kan utføre.
func motorHasTools(full map[string]any) bool {
	tools, _ := full["tools"].([]any)
	return len(tools) > 0
}
