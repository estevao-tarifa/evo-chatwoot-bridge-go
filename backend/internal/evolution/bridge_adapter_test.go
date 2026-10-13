package evolution

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/bridge"
)

func TestBridgeAdapter_SendText(t *testing.T) {
	t.Run("success maps the message id", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assertRequest(t, r, http.MethodPost, "/send/text", testToken)
			writeJSON(w, `{"data":{"Info":{"ID":"3EB0GENERATED0000000001"}}}`)
		})
		got, err := NewBridgeAdapter(c).SendText(t.Context(), "5511900000001", "hello")
		if err != nil {
			t.Fatalf("SendText() error = %v", err)
		}
		if got == nil || got.MessageID != "3EB0GENERATED0000000001" {
			t.Fatalf("SendText() = %+v, want the sent id", got)
		}
	})

	t.Run("a 5xx send is unconfirmed", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		})
		got, err := NewBridgeAdapter(c).SendText(t.Context(), "5511900000001", "hello")
		if got != nil {
			t.Fatalf("SendText() = %+v, want nil", got)
		}
		if !errors.Is(err, bridge.ErrUnconfirmed) {
			t.Fatalf("SendText() error = %v, want bridge.ErrUnconfirmed", err)
		}
	})

	t.Run("a 2xx without an id is unconfirmed", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, `{"message":"success"}`)
		})
		if _, err := NewBridgeAdapter(c).SendText(t.Context(), "5511900000001", "hello"); !errors.Is(err, bridge.ErrUnconfirmed) {
			t.Fatalf("SendText() error = %v, want bridge.ErrUnconfirmed", err)
		}
	})

	t.Run("a pre-flight rejection is not unconfirmed", func(t *testing.T) {
		c := newTestClient(t, func(http.ResponseWriter, *http.Request) {
			t.Error("no request must be made for an oversized send")
		})
		oversized := strings.Repeat("x", maxRequestBytes+1)
		if _, err := NewBridgeAdapter(c).SendText(t.Context(), "5511900000001", oversized); errors.Is(err, bridge.ErrUnconfirmed) {
			t.Fatalf("SendText() error = %v, want a known-not-sent error", err)
		}
	})
}
