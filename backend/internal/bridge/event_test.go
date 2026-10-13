package bridge

import (
	"testing"
)

func TestParse_PascalCase(t *testing.T) {
	payload := []byte(`{
		"event": "Message",
		"data": {
			"Info": {"Chat": "5511900000001@s.whatsapp.net", "Sender": "5511900000001@s.whatsapp.net",
				"ID": "ABC123", "IsFromMe": false, "IsGroup": false, "PushName": "Ana", "MediaType": "text"},
			"Message": {"conversation": "hello"}
		}
	}`)
	got, err := Parse(payload)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := ParsedEvent{
		EventName: "Message", MessageID: "ABC123",
		ChatJID: "5511900000001@s.whatsapp.net", SenderJID: "5511900000001@s.whatsapp.net",
		PushName: "Ana", MediaType: "text", MessageType: MessageTypeText, Text: "hello",
	}
	if *got != want {
		t.Errorf("Parse() = %+v, want %+v", *got, want)
	}
}

func TestParse_camelCase(t *testing.T) {
	payload := []byte(`{
		"event": "messages.upsert",
		"data": {
			"info": {"chat": "5511900000001@s.whatsapp.net", "id": "XYZ", "isFromMe": true, "pushName": "Ana"},
			"key": {"remoteJid": "5511900000001@s.whatsapp.net", "fromMe": true, "id": "XYZ"},
			"message": {"conversation": "hi"}
		}
	}`)
	got, err := Parse(payload)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.ChatJID != "5511900000001@s.whatsapp.net" || got.MessageID != "XYZ" || !got.IsFromMe || got.Text != "hi" {
		t.Errorf("Parse() = %+v", *got)
	}
}

func TestParse_IsGroupFromJID(t *testing.T) {
	payload := []byte(`{"event":"Message","data":{"Info":{"Chat":"120363000000000001@g.us","ID":"g1"},
		"groupData":{"Name":"Equipe X"},"Message":{"conversation":"oi"}}}`)
	got, err := Parse(payload)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !got.IsGroup {
		t.Error("IsGroup = false, want true (derived from @g.us)")
	}
	if got.GroupName != "Equipe X" {
		t.Errorf("GroupName = %q, want Equipe X", got.GroupName)
	}
}

func TestParse_GroupNameAbsent(t *testing.T) {
	payload := []byte(`{"event":"Message","data":{"Info":{"Chat":"120363000000000001@g.us","ID":"g2"},
		"Message":{"conversation":"oi"}}}`)
	got, err := Parse(payload)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.GroupName != "" {
		t.Errorf("GroupName = %q, want empty", got.GroupName)
	}
}

func TestParse_MessageTypePriority(t *testing.T) {
	cases := map[string]struct {
		message string
		kind    string
	}{
		"conversation":      {`{"conversation":"x"}`, MessageTypeText},
		"extended text":     {`{"extendedTextMessage":{"text":"x"}}`, MessageTypeText},
		"image":             {`{"imageMessage":{"caption":"c"}}`, MessageTypeImage},
		"audio":             {`{"audioMessage":{}}`, MessageTypeAudio},
		"document":          {`{"documentMessage":{"caption":"c"}}`, MessageTypeDocument},
		"sticker":           {`{"stickerMessage":{}}`, MessageTypeSticker},
		"reaction":          {`{"reactionMessage":{}}`, MessageTypeReaction},
		"protocol":          {`{"protocolMessage":{}}`, MessageTypeProtocol},
		"contact":           {`{"contactMessage":{}}`, MessageTypeContact},
		"location":          {`{"locationMessage":{}}`, MessageTypeLocation},
		"unknown":           {`{}`, MessageTypeUnknown},
		"conversation wins": {`{"conversation":"first","imageMessage":{}}`, MessageTypeText},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kind, _ := parseMessage([]byte(tc.message))
			if kind != tc.kind {
				t.Errorf("parseMessage(%s) = %q, want %q", tc.message, kind, tc.kind)
			}
		})
	}
}

func TestParse_Malformed(t *testing.T) {
	for name, payload := range map[string]string{
		"not json":     `{`,
		"empty":        ``,
		"null payload": `null`,
		"no data":      `{"event":"Message"}`,
		"null data":    `{"event":"Message","data":null}`,
		"bad data":     `{"event":"Message","data":123}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(payload)); err == nil {
				t.Error("Parse() error = nil, want error")
			}
		})
	}
}
