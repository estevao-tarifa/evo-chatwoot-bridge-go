// Package auth owns the admin session model (ADR-0006 §1.4): password
// verification against admin_users, the stateless HMAC session cookie (no
// bearer token) and the in-memory login rate limiter.
//
// The session token is base64url(admin_id|exp|token_version|instance_nonce) +
// "." + base64url(HMAC-SHA256(SESSION_SECRET)). Revocation is in-process:
// logout bumps an in-memory, per-admin token_version counter, and every process
// carries a random instance nonce, so a restart invalidates every previously
// issued session — a logged-out token cannot be resurrected by a reset counter.
// The baseline admin_users table has no token_version column and Phase 5 ships
// no migration (spec §6.5 R4), so the counter is per-instance (the same
// documented limitation as the rate limiter) until a future approved migration.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the password hash cost (ADR-0006 §2: cost 12, legacy used 10).
const BcryptCost = 12

// DefaultSessionTTL is the session lifetime (ADR-0006 §1.4: 7 days).
const DefaultSessionTTL = 7 * 24 * time.Hour

// Login rate limiting (ADR-0006 §4).
const (
	maxLoginFailures = 5
	loginWindow      = 15 * time.Minute
)

// minPasswordLength mirrors the legacy setup rule (password < 6 rejected).
const minPasswordLength = 6

// Sentinel errors the HTTP layer maps to status codes.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrInactive           = errors.New("auth: inactive admin")
	ErrNoSession          = errors.New("auth: no session")
	ErrRateLimited        = errors.New("auth: rate limited")
	ErrSetupClosed        = errors.New("auth: setup already completed")
	ErrConflict           = errors.New("auth: admin already exists")
	ErrInvalidRequest     = errors.New("auth: invalid request")
)

// Admin is the bridge-owned view of one admin_users row. PasswordHash is a
// credential: it never reaches a log or a response body.
type Admin struct {
	ID           int
	Username     string
	PasswordHash string
	DisplayName  string
	IsActive     bool
}

// Session is the authenticated admin identity resolved from a cookie.
type Session struct {
	ID          int
	Username    string
	DisplayName string
}

// Store is the consumer-side port over admin_users (satisfied by
// internal/storage/mysql.AdminUsers). A missing row is (nil, nil).
type Store interface {
	Count(ctx context.Context) (int, error)
	FindByUsername(ctx context.Context, username string) (*Admin, error)
	FindByID(ctx context.Context, id int) (*Admin, error)
	Create(ctx context.Context, username, passwordHash, displayName string) (*Admin, error)
	TouchLastLogin(ctx context.Context, id int) error
}

// Config configures the Service.
type Config struct {
	// Secret is SESSION_SECRET. An empty secret makes every session invalid:
	// the service never issues or accepts a token without it.
	Secret string
	TTL    time.Duration
	Now    func() time.Time
}

// Service implements the admin session model. It is safe for concurrent use.
type Service struct {
	store  Store
	secret []byte
	// nonce is the random per-process instance nonce signed into every token.
	// A new process gets a new nonce, so a restart rejects every token issued
	// before it (ADR-0006 Amendment 5).
	nonce string
	ttl   time.Duration
	now   func() time.Time

	// setupMu serialises the first-run Setup count-then-create so two concurrent
	// calls in one process cannot both observe an empty admin_users and each
	// create an admin. It is deliberately separate from mu, which only guards the
	// in-memory version map and must not be held across the DB calls/bcrypt work.
	//
	// It does NOT span processes: admin_users has no singleton constraint and
	// Phase 5 ships no migration (spec §6.5 R4), so two replicas can still each
	// create a distinct first admin. Deploy a single writer (or sticky sessions)
	// until an approved migration adds the constraint.
	setupMu sync.Mutex

	mu      sync.Mutex
	version map[int]int64
	limit   *limiter
}

// New returns a Service over store. A nil store is a programming error and
// panics. A crypto/rand failure is fatal: without a nonce the restart
// guarantee cannot hold, so the process must not start.
func New(store Store, cfg Config) *Service {
	if store == nil {
		panic("auth: New requires a non-nil Store")
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	nonce, err := newInstanceNonce()
	if err != nil {
		panic("auth: cannot read crypto/rand for the session instance nonce: " + err.Error())
	}
	return &Service{
		store:   store,
		secret:  []byte(cfg.Secret),
		nonce:   nonce,
		ttl:     ttl,
		now:     now,
		version: map[int]int64{},
		limit:   newLimiter(maxLoginFailures, loginWindow, now),
	}
}

// State reports "needs_setup" while admin_users is empty and "ready" otherwise.
func (s *Service) State(ctx context.Context) (string, error) {
	count, err := s.store.Count(ctx)
	if err != nil {
		return "", fmt.Errorf("auth: counting admins: %w", err)
	}
	if count == 0 {
		return "needs_setup", nil
	}
	return "ready", nil
}

// Setup creates the first admin. It is accepted only while admin_users is empty
// (legacy parity, ADR-0006 §2). It returns the new session and its cookie token.
// The count-then-create is serialised per process (setupMu), so two concurrent
// distinct-username setups in one instance produce exactly one admin; see
// setupMu for the cross-process caveat.
func (s *Service) Setup(ctx context.Context, username, password, displayName string) (*Session, string, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(password) < minPasswordLength {
		return nil, "", ErrInvalidRequest
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	count, err := s.store.Count(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("auth: counting admins: %w", err)
	}
	if count > 0 {
		return nil, "", ErrSetupClosed
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return nil, "", fmt.Errorf("auth: hashing password: %w", err)
	}
	admin, err := s.store.Create(ctx, username, string(hash), strings.TrimSpace(displayName))
	if err != nil {
		return nil, "", fmt.Errorf("auth: creating admin: %w", err)
	}
	return s.issue(admin)
}

// Login verifies credentials and issues a session. Unknown user and wrong
// password are indistinguishable (ErrInvalidCredentials) so there is no user
// enumeration signal. A keyed in-memory limiter returns ErrRateLimited.
func (s *Service) Login(ctx context.Context, clientKey, username, password string) (*Session, string, time.Duration, error) {
	username = strings.TrimSpace(username)
	key := clientKey + "\x00" + username
	if retryAfter, ok := s.limit.blocked(key); ok {
		return nil, "", retryAfter, ErrRateLimited
	}
	if username == "" || password == "" {
		return nil, "", 0, ErrInvalidRequest
	}

	admin, err := s.store.FindByUsername(ctx, username)
	if err != nil {
		return nil, "", 0, fmt.Errorf("auth: finding admin: %w", err)
	}
	if admin == nil || !passwordMatches(admin.PasswordHash, password) {
		s.limit.failure(key)
		return nil, "", 0, ErrInvalidCredentials
	}
	if !admin.IsActive {
		// An inactive admin is a real state, distinct from bad credentials, but
		// still counts as a failure so it cannot be probed freely.
		s.limit.failure(key)
		return nil, "", 0, ErrInactive
	}
	s.limit.reset(key)
	if err := s.store.TouchLastLogin(ctx, admin.ID); err != nil {
		// A failed lastLoginAt write must not deny a valid login.
		_ = err
	}
	sess, token, err := s.issue(admin)
	return sess, token, 0, err
}

// Logout revokes the sessions of the token's admin (best effort) and is
// idempotent: an absent, expired or tampered token is not an error.
func (s *Service) Logout(token string) {
	id, _, err := s.decode(token)
	if err != nil {
		return
	}
	s.RevokeSessions(id)
}

// Authenticate resolves a cookie token to a Session. Every failure (missing,
// tampered, expired, revoked, unknown or inactive admin) is ErrNoSession so the
// caller answers a uniform 401.
func (s *Service) Authenticate(ctx context.Context, token string) (*Session, error) {
	id, ver, err := s.decode(token)
	if err != nil {
		return nil, ErrNoSession
	}
	if s.currentVersion(id) != ver {
		return nil, ErrNoSession
	}
	admin, err := s.store.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("auth: finding admin: %w", err)
	}
	if admin == nil || !admin.IsActive {
		return nil, ErrNoSession
	}
	return &Session{ID: admin.ID, Username: admin.Username, DisplayName: admin.DisplayName}, nil
}

// VerifyPassword reports whether plain matches the stored bcrypt hash. It is
// exported for tests and for future password-change support.
func VerifyPassword(hash, plain string) bool { return passwordMatches(hash, plain) }

// issue builds a session and its signed token at the admin's current version.
func (s *Service) issue(admin *Admin) (*Session, string, error) {
	sess := &Session{ID: admin.ID, Username: admin.Username, DisplayName: admin.DisplayName}
	exp := s.now().Add(s.ttl).Unix()
	token, err := s.encode(admin.ID, exp, s.currentVersion(admin.ID))
	if err != nil {
		return nil, "", err
	}
	return sess, token, nil
}

// currentVersion returns the in-memory session version for an admin id.
func (s *Service) currentVersion(id int) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version[id]
}

// RevokeSessions bumps the in-memory token version for an admin, invalidating
// every session issued before the call. Logout calls it, and any password
// change must call it too. The counter is per-instance (see the package doc):
// only a restart invalidates sessions across processes.
func (s *Service) RevokeSessions(adminID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.version[adminID]++
}

// encode signs the payload "id|exp|version|nonce" with HMAC-SHA256.
func (s *Service) encode(id int, exp, version int64) (string, error) {
	if len(s.secret) == 0 {
		return "", ErrNoSession
	}
	payload := strconv.Itoa(id) + "|" + strconv.FormatInt(exp, 10) + "|" +
		strconv.FormatInt(version, 10) + "|" + s.nonce
	return s.sign([]byte(payload)), nil
}

// newInstanceNonce returns the random per-process nonce embedded in every
// session token. A new process gets a new nonce, so a restart invalidates every
// token issued before it — including one whose in-memory version counter reset
// to zero.
func newInstanceNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// sign returns base64url(payload) + "." + base64url(HMAC).
func (s *Service) sign(payload []byte) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// decode verifies the signature and expiry and returns the admin id and
// version. Every failure is ErrNoSession.
func (s *Service) decode(token string) (int, int64, error) {
	if len(s.secret) == 0 || token == "" {
		return 0, 0, ErrNoSession
	}
	dot := strings.LastIndexByte(token, '.')
	if dot <= 0 || dot == len(token)-1 {
		return 0, 0, ErrNoSession
	}
	payload, err := base64.RawURLEncoding.DecodeString(token[:dot])
	if err != nil {
		return 0, 0, ErrNoSession
	}
	signature, err := base64.RawURLEncoding.DecodeString(token[dot+1:])
	if err != nil {
		return 0, 0, ErrNoSession
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	if subtle.ConstantTimeCompare(signature, mac.Sum(nil)) != 1 {
		return 0, 0, ErrNoSession
	}

	parts := strings.Split(string(payload), "|")
	if len(parts) != 4 {
		return 0, 0, ErrNoSession
	}
	id, err := strconv.Atoi(parts[0])
	if err != nil || id <= 0 {
		return 0, 0, ErrNoSession
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || s.now().Unix() >= exp {
		return 0, 0, ErrNoSession
	}
	version, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return 0, 0, ErrNoSession
	}
	// A token minted by another process carries a different instance nonce and
	// is rejected: a restart invalidates every previously issued session.
	if parts[3] != s.nonce {
		return 0, 0, ErrNoSession
	}
	return id, version, nil
}

// passwordMatches compares a bcrypt hash in constant time. A malformed hash is
// never a match.
func passwordMatches(hash, plain string) bool {
	if hash == "" || plain == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// limiter is the in-memory, per-instance login failure limiter (ADR-0006 §4).
type limiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	now      func() time.Time
	attempts map[string]*attempt
}

type attempt struct {
	count int
	first time.Time
}

func newLimiter(max int, window time.Duration, now func() time.Time) *limiter {
	return &limiter{max: max, window: window, now: now, attempts: map[string]*attempt{}}
}

// blocked reports whether key is over the limit, with the remaining window.
func (l *limiter) blocked(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[key]
	if !ok {
		return 0, false
	}
	elapsed := l.now().Sub(a.first)
	if elapsed >= l.window {
		delete(l.attempts, key)
		return 0, false
	}
	if a.count < l.max {
		return 0, false
	}
	return l.window - elapsed, true
}

// failure records one failed attempt, starting a fresh window when expired.
func (l *limiter) failure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[key]
	if !ok || l.now().Sub(a.first) >= l.window {
		l.attempts[key] = &attempt{count: 1, first: l.now()}
		return
	}
	a.count++
}

// reset clears a key after a successful login.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
