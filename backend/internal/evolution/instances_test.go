package evolution

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestResolveInstanceToken(t *testing.T) {
	tests := []struct {
		name      string
		response  string
		wantToken string
		wantErrIs error
	}{
		{
			name:      "evolution go flat shape",
			response:  `{"message":"success","data":[{"id":"1","name":"other","token":"other-token"},{"id":"2","name":"synthetic-instance","token":"resolved-token"}]}`,
			wantToken: "resolved-token",
		},
		{
			name:      "v2 nested flat array",
			response:  `[{"instance":{"instanceName":"synthetic-instance","apikey":"v2-token","status":"open"}}]`,
			wantToken: "v2-token",
		},
		{
			name:      "v2 nested in data envelope",
			response:  `{"data":[{"instance":{"instanceName":"synthetic-instance","apikey":"v2b-token"}}]}`,
			wantToken: "v2b-token",
		},
		{
			// auth.md §3.2: `data?.data || data?.instances || []`.
			name:      "instances envelope",
			response:  `{"instances":[{"name":"other","token":"x"},{"name":"synthetic-instance","token":"inst-token"}]}`,
			wantToken: "inst-token",
		},
		{
			name:      "not found",
			response:  `{"data":[{"name":"other","token":"x"}]}`,
			wantErrIs: ErrInstanceNotFound,
		},
		{
			name:      "empty data list",
			response:  `{"data":[]}`,
			wantErrIs: ErrInstanceNotFound,
		},
		{
			name:      "found without token",
			response:  `{"data":[{"name":"synthetic-instance"}]}`,
			wantErrIs: ErrInstanceTokenMissing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				assertRequest(t, r, http.MethodGet, "/instance/all", testGlobalKey)
				writeJSON(w, tt.response)
			})

			token, err := c.ResolveInstanceToken(t.Context())
			if tt.wantErrIs != nil {
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("ResolveInstanceToken() error = %v, want %v", err, tt.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveInstanceToken() error = %v", err)
			}
			if token != tt.wantToken {
				t.Errorf("token = %q, want %q", token, tt.wantToken)
			}
		})
	}
}

func TestErrorsDoNotLeakInstanceToken(t *testing.T) {
	const token = "secret-token-value"
	c := newTestClientWithToken(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"apikey secret-token-value rejected"}`))
	}, token)

	_, err := c.GetInstanceStatus(t.Context())
	if err == nil {
		t.Fatal("GetInstanceStatus() error = nil, want error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked instance token: %v", err)
	}
}

func TestResolveInstanceTokenUpdatesInstanceCalls(t *testing.T) {
	var statusKey string
	c := newTestClientWithToken(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/instance/all":
			assertRequest(t, r, http.MethodGet, "/instance/all", testGlobalKey)
			writeJSON(w, `{"data":[{"name":"synthetic-instance","token":"resolved-token"}]}`)
		case "/instance/status":
			statusKey = r.Header.Get(headerAPIKey)
			writeJSON(w, `{"state":"open"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}, "")

	if _, err := c.ResolveInstanceToken(t.Context()); err != nil {
		t.Fatalf("ResolveInstanceToken() error = %v", err)
	}
	if _, err := c.GetInstanceStatus(t.Context()); err != nil {
		t.Fatalf("GetInstanceStatus() error = %v", err)
	}
	if statusKey != "resolved-token" {
		t.Errorf("instance call apikey = %q, want resolved-token", statusKey)
	}
}

// TestGlobalKeyFallbackOnInstanceRoutes covers the legacy single-key deployment:
// with no instance token configured, instance routes must send the global key
// (auth.md §1, §5).
func TestGlobalKeyFallbackOnInstanceRoutes(t *testing.T) {
	t.Run("GetInstanceStatus", func(t *testing.T) {
		c := newTestClientWithToken(t, func(w http.ResponseWriter, r *http.Request) {
			assertRequest(t, r, http.MethodGet, "/instance/status", testGlobalKey)
			writeJSON(w, `{"state":"open"}`)
		}, "")
		if _, err := c.GetInstanceStatus(t.Context()); err != nil {
			t.Fatalf("GetInstanceStatus() error = %v", err)
		}
	})

	t.Run("SendText", func(t *testing.T) {
		c := newTestClientWithToken(t, func(w http.ResponseWriter, r *http.Request) {
			assertRequest(t, r, http.MethodPost, "/send/text", testGlobalKey)
			writeJSON(w, `{"data":{"Info":{"ID":"x"}}}`)
		}, "")
		if _, err := c.SendText(t.Context(), "5511900000001", "hi", SendOptions{}); err != nil {
			t.Fatalf("SendText() error = %v", err)
		}
	})
}

func TestListInstances(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertRequest(t, r, http.MethodGet, "/instance/all", testGlobalKey)
		writeJSON(w, `{"message":"success","data":[{"name":"a","token":"ta"},{"instance":{"instanceName":"b","apikey":"tb"}}]}`)
	})

	instances, err := c.ListInstances(t.Context())
	if err != nil {
		t.Fatalf("ListInstances() error = %v", err)
	}
	want := []Instance{{Name: "a", Token: "ta"}, {Name: "b", Token: "tb"}}
	if len(instances) != len(want) {
		t.Fatalf("len = %d, want %d", len(instances), len(want))
	}
	for i := range want {
		if instances[i] != want[i] {
			t.Errorf("instances[%d] = %+v, want %+v", i, instances[i], want[i])
		}
	}
}

func TestGetInstanceStatus(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		assertRequest(t, r, http.MethodGet, "/instance/status", testToken)
		writeJSON(w, `{"state":"open","message":"connected"}`)
	})

	status, err := c.GetInstanceStatus(t.Context())
	if err != nil {
		t.Fatalf("GetInstanceStatus() error = %v", err)
	}
	if status["state"] != "open" {
		t.Errorf("state = %v, want open", status["state"])
	}
}

func TestFetchContacts(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantLen  int
	}{
		{name: "data envelope", response: `{"data":[{"jid":"1"},{"jid":"2"}]}`, wantLen: 2},
		{name: "flat array", response: `[{"jid":"1"}]`, wantLen: 1},
		{name: "contacts envelope", response: `{"contacts":[{"jid":"1"},{"jid":"2"},{"jid":"3"}]}`, wantLen: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				assertRequest(t, r, http.MethodGet, "/user/contacts", testToken)
				writeJSON(w, tt.response)
			})
			contacts, err := c.FetchContacts(t.Context())
			if err != nil {
				t.Fatalf("FetchContacts() error = %v", err)
			}
			if len(contacts) != tt.wantLen {
				t.Errorf("len = %d, want %d", len(contacts), tt.wantLen)
			}
		})
	}
}
