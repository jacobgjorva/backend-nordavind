package motor

// MethodKey identifiserer én oppgaveklasse. Katalogen selv (tekstene og
// budsjettene) kommer i del 2 — her står bare typen, så kontraktene i
// motor.go og gate.go kan referere den.
type MethodKey string

// MethodNone er fravær av metode: naken løkke, dagens adferd. Alt som ikke
// er trygt klassifisert havner her (fail-open).
const MethodNone MethodKey = ""
