package bridge

import (
	"strings"
	"unicode"
)

// minPhoneDigits is the shortest all-digit run accepted as a 1:1 identity,
// mirroring the legacy E14 guard (behaviours-to-preserve §B4, ROUTING_MATRIX E14).
const minPhoneDigits = 5

// Identity is the stable, connection-scoped identity of an event's peer.
type Identity struct {
	Kind        Kind
	Key         string // direct: phone digits; group: the full JID
	DisplayName string // authoritative non-placeholder name, or ""
}

// ParseIdentity derives the stable identity and rejects malformed input (E14):
// an empty JID, a too-short value, an "@" left in the phone slot, or no digits.
// Validation happens before any port is touched.
func ParseIdentity(p *ParsedEvent) (Identity, error) {
	chat := strings.TrimSpace(p.ChatJID)
	if chat == "" {
		return Identity{}, ErrMalformedIdentity
	}

	if p.IsGroup {
		if !isGroupJID(chat) || len(digitsOnly(phonePart(chat))) < minPhoneDigits {
			return Identity{}, ErrMalformedIdentity
		}
		return Identity{Kind: KindGroup, Key: chat, DisplayName: groupDisplayName(p.GroupName, chat)}, nil
	}

	phone := phonePart(chat)
	if phone == "" || strings.Contains(phone, "@") {
		return Identity{}, ErrMalformedIdentity
	}
	digits := digitsOnly(phone)
	if len(digits) < minPhoneDigits {
		return Identity{}, ErrMalformedIdentity
	}
	return Identity{Kind: KindDirect, Key: digits, DisplayName: directDisplayName(p.PushName, digits)}, nil
}

// isGroupJID reports whether jid is a WhatsApp group JID.
func isGroupJID(jid string) bool {
	return strings.Contains(jid, "@g.us")
}

// isBroadcastJID reports whether jid is a status/broadcast or newsletter JID
// (E7: stories and channels are ignored).
func isBroadcastJID(jid string) bool {
	return strings.Contains(jid, "@broadcast") || strings.Contains(jid, "@newsletter")
}

// phonePart strips a JID's "@domain" suffix, mirroring the legacy extractPhone.
func phonePart(jid string) string {
	if i := strings.Index(jid, "@"); i >= 0 {
		return jid[:i]
	}
	return jid
}

// digitsOnly strips every non-digit rune.
func digitsOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// isCorrelatableMessageID reports whether id is a real destination message id
// the echo store can correlate. An empty, whitespace-only or zero-width/format
// -only id (U+200B/U+200C/U+200D/U+FEFF and other Unicode space/format runes) is
// not a usable correlation key: a send returning one is an unknown mutation
// outcome, never a confirmed delivery (ADR-0004 §5, rule 6).
func isCorrelatableMessageID(id string) bool {
	for _, r := range id {
		if !unicode.IsSpace(r) && !unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

// groupDisplayName returns the authoritative group name, or "" when the payload
// has none. A group name is never inferred from PushName, a phone or the JID,
// and the placeholder "Grupo" is rejected (AGENTS §7 rule 4, ADR-0009 §1).
func groupDisplayName(name, jid string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "Grupo") || name == jid || strings.Contains(name, "@") {
		return ""
	}
	if looksLikePhone(name) {
		return ""
	}
	return name
}

// directDisplayName returns a usable 1:1 contact name, or "" when the push name
// is absent or is just the phone/JID.
func directDisplayName(pushName, digits string) string {
	pushName = strings.TrimSpace(pushName)
	if pushName == "" || pushName == digits || strings.Contains(pushName, "@") {
		return ""
	}
	if looksLikePhone(pushName) {
		return ""
	}
	return pushName
}

// looksLikePhone reports whether name is a phone number, raw or formatted
// (spaces, dashes, dots, parentheses, an optional "+"). Digits are normalised
// first so "+55 11 90000-0001" is rejected as a display name, not accepted as
// one (AGENTS §7 rule 4).
func looksLikePhone(name string) bool {
	if len(digitsOnly(name)) < minPhoneDigits {
		return false
	}
	// Only digits and phone punctuation may remain; any letter or other rune
	// means it is a name, not a phone.
	return strings.Trim(name, "0123456789+ -().") == ""
}
