package api

import "strings"

// Emoji-policy (Jacob 2026-08-06): personligheten kan krydres med emoji, men
// KUN fra et lite kuratert sett, og sparsomt — maks én per melding. Prompten
// oppfordrer; koden garanterer. Alt utenfor settet strykes, og settet selv
// klippes til budsjettet, så en pratsom modell aldri kan tapetsere svar.

// allowedEmoji er hele det tillatte settet. ✌️ lagres uten variantvelger —
// oppslaget normaliserer.
var allowedEmoji = map[string]bool{
	"😤": true, "🙃": true, "🫠": true, "😌": true, "💀": true,
	"🤝": true, "🦄": true, "🥂": true, "✌": true,
}

const emojiPerMessage = 1

// isEmojiRune: hører runen til emoji-blokkene? Dekker de vanlige områdene
// pluss piktogrammer; vanlig tekst, norske tegn og tegnsetting berøres aldri.
func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF: // emoji, symboler, piktogrammer
		return true
	case r >= 0x2600 && r <= 0x27BF: // diverse symboler + dingbats (✌ m.fl.)
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // piler/stjerner (⭐ m.fl.)
		return true
	case r == 0x2764: // ❤
		return true
	}
	return false
}

// enforceEmojiPolicy renser en hel melding: fjerner emoji utenfor settet
// (inkl. hudtoner og ZWJ-sekvenser) og beholder maks `budget` fra settet.
// Budsjettet deles på tvers av kall via pekeren, så en streamet melding kan
// renses setning for setning.
func enforceEmojiPolicy(s string, budget *int) string {
	if s == "" {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if !isEmojiRune(r) {
			// Foreldreløse variantvelgere/ZWJ etter en strøket emoji skjules.
			if r == 0xFE0F || r == 0x200D {
				continue
			}
			b.WriteRune(r)
			continue
		}
		// Sluk hele sekvensen: grunn-emoji + evt. variantvelger, hudtone og
		// ZWJ-koblede etterfølgere, så en strøket sekvens aldri etterlater rusk.
		base := string(r)
		j := i
		for j+1 < len(rs) {
			n := rs[j+1]
			if n == 0xFE0F || (n >= 0x1F3FB && n <= 0x1F3FF) {
				j++
				continue
			}
			if n == 0x200D && j+2 < len(rs) {
				j += 2
				continue
			}
			break
		}
		plain := j == i || (j == i+1 && rs[i+1] == 0xFE0F) // uten hudtone/ZWJ
		if plain && allowedEmoji[base] && *budget > 0 {
			*budget--
			b.WriteString(base)
			if j == i+1 {
				b.WriteRune(0xFE0F)
			}
		}
		i = j
	}
	return b.String()
}

// enforceEmojiMessage er engangsvarianten for bufrede meldinger.
func enforceEmojiMessage(s string) string {
	budget := emojiPerMessage
	return enforceEmojiPolicy(s, &budget)
}
