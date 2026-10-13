package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// fakeStore is an in-memory admin_users repo. It never touches MySQL.
type fakeStore struct {
	mu      sync.Mutex
	nextID  int
	rows    []*Admin
	findErr error
}

func newFakeStore() *fakeStore { return &fakeStore{nextID: 1} }

func (f *fakeStore) Count(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows), nil
}

func (f *fakeStore) FindByUsername(_ context.Context, username string) (*Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.findErr != nil {
		return nil, f.findErr
	}
	for _, a := range f.rows {
		if a.Username == username {
			clone := *a
			return &clone, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) FindByID(_ context.Context, id int) (*Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.findErr != nil {
		return nil, f.findErr
	}
	for _, a := range f.rows {
		if a.ID == id {
			clone := *a
			return &clone, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) Create(_ context.Context, username, hash, displayName string) (*Admin, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &Admin{ID: f.nextID, Username: username, PasswordHash: hash, DisplayName: displayName, IsActive: true}
	f.nextID++
	f.rows = append(f.rows, a)
	clone := *a
	return &clone, nil
}

func (f *fakeStore) TouchLastLogin(context.Context, int) error { return nil }

func (f *fakeStore) seed(username, password string, active bool) *Admin {
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	f.mu.Lock()
	defer f.mu.Unlock()
	a := &Admin{ID: f.nextID, Username: username, PasswordHash: string(hash), IsActive: active}
	f.nextID++
	f.rows = append(f.rows, a)
	return a
}

const testSecret = "synthetic-session-secret-0123456789abcdef"

func newTestService(store Store, now func() time.Time) *Service {
	return New(store, Config{Secret: testSecret, Now: now})
}

func TestSetupOnlyWhenEmpty(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := newTestService(store, time.Now)

	sess, token, err := svc.Setup(ctx, "admin", "secret1", "Admin")
	if err != nil || sess == nil || token == "" {
		t.Fatalf("Setup() = %v, %v, err=%v", sess, token, err)
	}
	if sess.Username != "admin" || sess.DisplayName != "Admin" {
		t.Errorf("session = %+v", sess)
	}

	if _, _, err := svc.Setup(ctx, "second", "secret1", ""); !errors.Is(err, ErrSetupClosed) {
		t.Errorf("second Setup() err = %v, want ErrSetupClosed", err)
	}

	state, err := svc.State(ctx)
	if err != nil || state != "ready" {
		t.Errorf("State() = %q, %v, want ready", state, err)
	}
}

func TestSetupValidation(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(newFakeStore(), time.Now)
	for _, tt := range []struct{ name, user, pass string }{
		{"empty username", "", "secret1"},
		{"short password", "admin", "12345"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := svc.Setup(ctx, tt.user, tt.pass, ""); !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("Setup() err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

// TestSetupRaceOnlyOneWins proves the first-run count-then-create is serialised:
// two concurrent setups with distinct usernames (which the UNIQUE(username)
// index alone cannot stop) yield exactly one admin, the loser ErrSetupClosed.
// It fails if setupMu is removed, because both goroutines observe an empty
// admin_users before either Create commits (bcrypt cost 12 keeps the window
// open). The cross-process caveat is documented on setupMu.
func TestSetupRaceOnlyOneWins(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := newTestService(store, time.Now)

	const attempts = 2
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := svc.Setup(ctx, fmt.Sprintf("admin-%d", i), "secret1", "")
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	var wins, closed int
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrSetupClosed):
			closed++
		default:
			t.Errorf("unexpected Setup() error: %v", err)
		}
	}
	if wins != 1 || closed != attempts-1 {
		t.Errorf("setup race: wins=%d closed=%d, want 1 win and %d ErrSetupClosed", wins, closed, attempts-1)
	}
	if n, err := store.Count(ctx); err != nil || n != 1 {
		t.Errorf("admins created = %d, %v, want exactly 1", n, err)
	}
}

func TestBcryptCost(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := newTestService(store, time.Now)
	if _, _, err := svc.Setup(ctx, "admin", "secret1", ""); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	admin, err := store.FindByUsername(ctx, "admin")
	if err != nil || admin == nil {
		t.Fatalf("FindByUsername() = %v, %v", admin, err)
	}
	cost, err := bcrypt.Cost([]byte(admin.PasswordHash))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost != BcryptCost {
		t.Errorf("stored hash cost = %d, want %d", cost, BcryptCost)
	}
}

func TestLoginFailures(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.seed("active", "correct-horse", true)
	store.seed("inactive", "correct-horse", false)
	svc := newTestService(store, time.Now)

	t.Run("unknown user", func(t *testing.T) {
		_, _, _, err := svc.Login(ctx, "1.2.3.4", "ghost", "x")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("err = %v, want ErrInvalidCredentials", err)
		}
	})
	t.Run("wrong password", func(t *testing.T) {
		_, _, _, err := svc.Login(ctx, "1.2.3.5", "active", "wrong")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("err = %v, want ErrInvalidCredentials", err)
		}
	})
	t.Run("inactive admin", func(t *testing.T) {
		_, _, _, err := svc.Login(ctx, "1.2.3.6", "inactive", "correct-horse")
		if !errors.Is(err, ErrInactive) {
			t.Errorf("err = %v, want ErrInactive", err)
		}
	})
	t.Run("success", func(t *testing.T) {
		sess, token, _, err := svc.Login(ctx, "1.2.3.7", "active", "correct-horse")
		if err != nil || sess == nil || token == "" {
			t.Fatalf("Login() = %v, %v, %v", sess, token, err)
		}
	})
}

// TestLoginIdenticalBody proves unknown user and wrong password are
// indistinguishable: both return the same sentinel, so the HTTP layer emits an
// identical body.
func TestLoginIdenticalBody(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.seed("active", "correct-horse", true)
	svc := newTestService(store, time.Now)

	_, _, _, unknownErr := svc.Login(ctx, "9.9.9.1", "ghost", "x")
	_, _, _, wrongErr := svc.Login(ctx, "9.9.9.2", "active", "wrong")
	if unknownErr.Error() != wrongErr.Error() {
		t.Errorf("error strings differ: %q vs %q", unknownErr, wrongErr)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.seed("active", "correct-horse", true)
	now := time.Now()
	svc := newTestService(store, func() time.Time { return now })

	for i := range maxLoginFailures {
		if _, _, _, err := svc.Login(ctx, "5.5.5.5", "active", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("failure %d err = %v", i, err)
		}
	}
	if _, _, retryAfter, err := svc.Login(ctx, "5.5.5.5", "active", "wrong"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("6th attempt err = %v, want ErrRateLimited", err)
	} else if retryAfter <= 0 || retryAfter > loginWindow {
		t.Errorf("retryAfter = %v, want within (0, %v]", retryAfter, loginWindow)
	}

	// A different client key is unaffected.
	if _, _, _, err := svc.Login(ctx, "6.6.6.6", "active", "correct-horse"); err != nil {
		t.Errorf("other key err = %v, want nil", err)
	}

	// Success after the window resets the counter.
	now = now.Add(loginWindow + time.Second)
	if _, _, _, err := svc.Login(ctx, "5.5.5.5", "active", "correct-horse"); err != nil {
		t.Fatalf("post-window success err = %v", err)
	}
	if _, _, _, err := svc.Login(ctx, "5.5.5.5", "active", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("after reset err = %v, want ErrInvalidCredentials", err)
	}
}

func TestSessionRevocation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	admin := store.seed("active", "correct-horse", true)
	svc := newTestService(store, time.Now)

	sess, token, _, err := svc.Login(ctx, "1.1.1.1", "active", "correct-horse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if _, err := svc.Authenticate(ctx, token); err != nil {
		t.Fatalf("Authenticate() before logout = %v", err)
	}

	t.Run("tampered signature", func(t *testing.T) {
		if _, err := svc.Authenticate(ctx, token+"x"); !errors.Is(err, ErrNoSession) {
			t.Errorf("err = %v, want ErrNoSession", err)
		}
	})

	t.Run("bumped version", func(t *testing.T) {
		svc.RevokeSessions(admin.ID)
		if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
			t.Errorf("err = %v, want ErrNoSession", err)
		}
	})
	if sess.ID != admin.ID {
		t.Errorf("session id = %d, want %d", sess.ID, admin.ID)
	}
}

func TestLogoutRevokes(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.seed("active", "correct-horse", true)
	svc := newTestService(store, time.Now)

	_, token, _, err := svc.Login(ctx, "1.1.1.1", "active", "correct-horse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	svc.Logout(token)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("Authenticate() after logout err = %v, want ErrNoSession", err)
	}
	// Idempotent: a second logout of a bad token is not a panic/error.
	svc.Logout("not-a-token")
}

func TestExpiredSession(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.seed("active", "correct-horse", true)
	now := time.Now()
	svc := newTestService(store, func() time.Time { return now })

	_, token, _, err := svc.Login(ctx, "1.1.1.1", "active", "correct-horse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	now = now.Add(DefaultSessionTTL + time.Second)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("Authenticate() after expiry err = %v, want ErrNoSession", err)
	}
}

func TestPasswordChangeRevokes(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	admin := store.seed("active", "correct-horse", true)
	svc := newTestService(store, time.Now)

	_, token, _, err := svc.Login(ctx, "1.1.1.1", "active", "correct-horse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	// Simulate a password change: the stored hash no longer matches. The hash is
	// NOT embedded in the token, so revocation is not automatic — a
	// password-change handler must call RevokeSessions.
	store.mu.Lock()
	store.rows[0].PasswordHash = "changed"
	store.mu.Unlock()
	svc.RevokeSessions(admin.ID)
	if _, err := svc.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}

// TestRestartInvalidatesSessions proves a fresh Service (a new process, same
// secret and store) rejects a token issued before the restart: every token
// carries the per-process instance nonce, so a reset in-memory version counter
// cannot resurrect a logged-out token (A-MED-1, ADR-0006 Amendment 5).
func TestRestartInvalidatesSessions(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.seed("active", "correct-horse", true)
	before := newTestService(store, time.Now)

	_, token, _, err := before.Login(ctx, "1.1.1.1", "active", "correct-horse")
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	after := newTestService(store, time.Now)
	if _, err := after.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Errorf("pre-restart token accepted by a fresh instance: err = %v, want ErrNoSession", err)
	}
}

func TestEmptySecretRejectsSessions(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.seed("active", "correct-horse", true)
	svc := New(store, Config{Secret: "", Now: time.Now})
	if _, _, _, err := svc.Login(ctx, "1.1.1.1", "active", "correct-horse"); !errors.Is(err, ErrNoSession) {
		t.Errorf("Login() with empty secret err = %v, want ErrNoSession", err)
	}
	if _, err := svc.Authenticate(ctx, "anything"); !errors.Is(err, ErrNoSession) {
		t.Errorf("Authenticate() with empty secret err = %v, want ErrNoSession", err)
	}
}

// TestDecodeRejectsMalformed exercises the token parser directly.
func TestDecodeRejectsMalformed(t *testing.T) {
	svc := newTestService(newFakeStore(), time.Now)
	valid, err := svc.encode(1, time.Now().Add(time.Hour).Unix(), 0)
	if err != nil {
		t.Fatalf("encode() error = %v", err)
	}
	cases := map[string]string{
		"empty":             "",
		"no dot":            "abcdef",
		"trailing dot":      valid + ".",
		"bad payload b64":   "!!!." + strings.SplitN(valid, ".", 2)[1],
		"bad signature b64": strings.SplitN(valid, ".", 2)[0] + ".!!!",
		"wrong field count": svc.sign([]byte("1|2")),
		"non numeric id":    svc.sign([]byte("x|9999999999|0|" + svc.nonce)),
		"non positive id":   svc.sign([]byte("0|9999999999|0|" + svc.nonce)),
		"expired":           svc.sign([]byte("1|1|0|" + svc.nonce)),
		"wrong nonce":       svc.sign([]byte("1|9999999999|0|other-instance")),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := svc.decode(token); !errors.Is(err, ErrNoSession) {
				t.Errorf("decode(%q) err = %v, want ErrNoSession", token, err)
			}
		})
	}
	if _, _, err := svc.decode(valid); err != nil {
		t.Errorf("decode(valid) err = %v", err)
	}
}

func TestVerifyPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), BcryptCost)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(string(hash), "hunter2") {
		t.Error("VerifyPassword() = false, want true")
	}
	if VerifyPassword(string(hash), "wrong") {
		t.Error("VerifyPassword() = true for wrong password")
	}
	if VerifyPassword("", "x") || VerifyPassword("garbage", "x") {
		t.Error("VerifyPassword() accepted a malformed hash")
	}
}

func ExampleService_State() {
	store := newFakeStore()
	svc := New(store, Config{Secret: "s"})
	state, _ := svc.State(context.Background())
	fmt.Println(state)
	// Output: needs_setup
}
