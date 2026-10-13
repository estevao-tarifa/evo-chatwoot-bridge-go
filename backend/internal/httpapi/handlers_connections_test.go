package httpapi

import (
	"strings"
	"testing"
)

// TestWebhookSubscribeEvents_GOEnumPinned pins the exact subscription list the
// bridge sends to Evolution GO (POST /instance/connect). The values must be the
// GO 0.7.2 uppercase enum
// (MESSAGE,SEND_MESSAGE,READ_RECEIPT,PRESENCE,HISTORY_SYNC,CHAT_PRESENCE,CALL,
// CONNECTION,LABEL,CONTACT,GROUP,NEWSLETTER,QRCODE,BUTTON_CLICK,PICTURE,
// USER_ABOUT — docs/contracts/evolution/webhooks.md §2.1,
// [live-verified 2026-10-13]). GO discards any value outside that enum, so a
// silent reintroduction of the v2/Node `MESSAGES_UPSERT` (or any legacy
// PascalCase name) would leave the production instance subscribed to nothing.
func TestWebhookSubscribeEvents_GOEnumPinned(t *testing.T) {
	want := []string{
		"MESSAGE",
		"SEND_MESSAGE",
		"READ_RECEIPT",
		"PRESENCE",
		"HISTORY_SYNC",
		"CHAT_PRESENCE",
		"CALL",
		"CONNECTION",
		"LABEL",
		"CONTACT",
		"GROUP",
		"NEWSLETTER",
		"QRCODE",
		"BUTTON_CLICK",
		"PICTURE",
		"USER_ABOUT",
	}
	if len(webhookSubscribeEvents) != len(want) {
		t.Fatalf("len(webhookSubscribeEvents) = %d, want %d (%v)", len(webhookSubscribeEvents), len(want), webhookSubscribeEvents)
	}
	for i, name := range want {
		if webhookSubscribeEvents[i] != name {
			t.Errorf("webhookSubscribeEvents[%d] = %q, want %q", i, webhookSubscribeEvents[i], name)
		}
	}

	for _, name := range webhookSubscribeEvents {
		if name == "MESSAGES_UPSERT" {
			t.Errorf("webhookSubscribeEvents contains MESSAGES_UPSERT: it is an Evolution API v2/Node name, not a GO enum value, and GO discards it")
		}
		if name != strings.ToUpper(name) {
			t.Errorf("webhookSubscribeEvents contains %q: GO accepts only uppercase enum values", name)
		}
	}
}
