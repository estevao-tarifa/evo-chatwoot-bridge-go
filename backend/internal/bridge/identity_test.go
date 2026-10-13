package bridge

import (
	"errors"
	"testing"
)

func TestParseIdentity_Direct(t *testing.T) {
	got, err := ParseIdentity(&ParsedEvent{
		ChatJID: "5511900000001@s.whatsapp.net", SenderJID: "5511900000001@s.whatsapp.net", PushName: "Ana",
	})
	if err != nil {
		t.Fatalf("ParseIdentity() error = %v", err)
	}
	if got.Kind != KindDirect || got.Key != "5511900000001" || got.DisplayName != "Ana" {
		t.Errorf("ParseIdentity() = %+v", got)
	}
}

func TestParseIdentity_Group(t *testing.T) {
	got, err := ParseIdentity(&ParsedEvent{
		IsGroup: true, ChatJID: "120363000000000001@g.us", GroupName: "Equipe X",
	})
	if err != nil {
		t.Fatalf("ParseIdentity() error = %v", err)
	}
	if got.Kind != KindGroup || got.Key != "120363000000000001@g.us" || got.DisplayName != "Equipe X" {
		t.Errorf("ParseIdentity() = %+v", got)
	}
}

func TestParseIdentity_RejectsMalformed(t *testing.T) {
	cases := map[string]ParsedEvent{
		"empty jid":      {ChatJID: ""},
		"short direct":   {ChatJID: "1234"},
		"at in phone":    {ChatJID: "user@name"},
		"no digits":      {ChatJID: "abcdef"},
		"short group":    {IsGroup: true, ChatJID: "1@g.us"},
		"group not g.us": {IsGroup: true, ChatJID: "120363000000000001@s.whatsapp.net"},
	}
	for name, parsed := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseIdentity(&parsed); !errors.Is(err, ErrMalformedIdentity) {
				t.Errorf("ParseIdentity(%+v) error = %v, want ErrMalformedIdentity", parsed, err)
			}
		})
	}
}

func TestIsCorrelatableMessageID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "empty", id: "", want: false},
		{name: "space", id: " ", want: false},
		{name: "tab", id: "\t", want: false},
		{name: "newline", id: "\n", want: false},
		{name: "crlf", id: "\r\n", want: false},
		{name: "nbsp", id: "\u00a0", want: false},
		{name: "zero width space", id: "\u200b", want: false},
		{name: "zero width non-joiner", id: "\u200c", want: false},
		{name: "zero width joiner", id: "\u200d", want: false},
		{name: "bom", id: "\ufeff", want: false},
		{name: "mixed whitespace and zero width", id: "  \u200b  ", want: false},
		{name: "real id", id: "3EB0A1B2C3D4", want: true},
		{name: "id with surrounding spaces", id: " 3EB0A1B2C3D4 ", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCorrelatableMessageID(tt.id); got != tt.want {
				t.Errorf("isCorrelatableMessageID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestGroupDisplayName_RejectsPlaceholders(t *testing.T) {
	cases := map[string]string{
		"empty":           "",
		"grupo":           "Grupo",
		"GRUPO":           "grupo",
		"jid":             "120363000000000001@g.us",
		"phone":           "5511900000001",
		"plus phone":      "+5511900000001",
		"formatted phone": "+55 11 90000-0001",
		"parens phone":    "(55) 11 90000-0001",
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if got := groupDisplayName(candidate, "120363000000000001@g.us"); got != "" {
				t.Errorf("groupDisplayName(%q) = %q, want empty", candidate, got)
			}
		})
	}
	if got := groupDisplayName("Equipe X", "120363000000000001@g.us"); got != "Equipe X" {
		t.Errorf("groupDisplayName(Equipe X) = %q, want Equipe X", got)
	}
}

func TestLooksLikePhone_NormalisesFormattedPhones(t *testing.T) {
	for _, phone := range []string{
		"5511900000001",
		"+5511900000001",
		"+55 11 90000-0001",
		"55 11 90000 0001",
		"(55) 11 90000-0001",
		"+55.11.90000.0001",
	} {
		if !looksLikePhone(phone) {
			t.Errorf("looksLikePhone(%q) = false, want true", phone)
		}
	}
	for _, name := range []string{"Ana", "Equipe X", "Ana 1", "Suporte 24h"} {
		if looksLikePhone(name) {
			t.Errorf("looksLikePhone(%q) = true, want false (a real name)", name)
		}
	}
}
