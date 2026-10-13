package httpapi

import (
	"errors"
	"net/http"

	"github.com/estevao-tarifa/evo-chatwoot-bridge-go/backend/internal/auth"
)

type userDTO struct {
	ID          int    `json:"id,omitempty"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
}

type stateResponse struct {
	State string `json:"state"`
}

type successResponse struct {
	Success bool `json:"success"`
}

type successUserResponse struct {
	Success bool    `json:"success"`
	User    userDTO `json:"user"`
}

type meResponse struct {
	User *userDTO `json:"user"`
}

type credentialsRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

// authState reports whether first-run setup is still required (public).
func (s *server) authState(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	state, err := s.admin.Auth.State(r.Context())
	if err != nil {
		internalError(w, s.log(r), err)
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, stateResponse{State: state})
}

// authSetup creates the first admin, only while admin_users is empty (public).
func (s *server) authSetup(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	var req credentialsRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	sess, token, err := s.admin.Auth.Setup(r.Context(), req.Username, req.Password, req.DisplayName)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidRequest):
			invalidRequest(w, s.log(r), "username and a password of at least 6 characters are required")
		case errors.Is(err, auth.ErrConflict):
			// A concurrent first-run Setup won the race (UNIQUE username): a
			// clean 409 instead of a raw 500.
			conflict(w, s.log(r), "setup already completed")
		case errors.Is(err, auth.ErrSetupClosed):
			forbidden(w, s.log(r), "setup already completed")
		default:
			internalError(w, s.log(r), err)
		}
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(s.log(r), w, http.StatusOK, successUserResponse{
		Success: true,
		User:    userDTO{Username: sess.Username, DisplayName: sess.DisplayName},
	})
}

// authLogin verifies credentials and issues a session (public, rate limited).
func (s *server) authLogin(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	var req credentialsRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	sess, token, retryAfter, err := s.admin.Auth.Login(r.Context(), remoteAddr(r), req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			rateLimited(w, s.log(r), int(retryAfter.Seconds()))
		case errors.Is(err, auth.ErrInvalidCredentials):
			// Identical body for unknown user and wrong password (no enumeration).
			unauthorized(w, s.log(r))
		case errors.Is(err, auth.ErrInvalidRequest):
			invalidRequest(w, s.log(r), "username and password are required")
		case errors.Is(err, auth.ErrInactive):
			forbidden(w, s.log(r), "account is inactive")
		default:
			internalError(w, s.log(r), err)
		}
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(s.log(r), w, http.StatusOK, successUserResponse{
		Success: true,
		User:    userDTO{Username: sess.Username, DisplayName: sess.DisplayName},
	})
}

// authLogout revokes the session and clears the cookie (public, idempotent).
func (s *server) authLogout(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	s.admin.Auth.Logout(sessionToken(r))
	s.clearSessionCookie(w)
	writeJSON(s.log(r), w, http.StatusOK, successResponse{Success: true})
}

// authMe returns the current session's user, or null (200) when unauthenticated.
func (s *server) authMe(w http.ResponseWriter, r *http.Request) {
	if s.adminUnavailable(w, r) {
		return
	}
	sess, err := s.admin.Auth.Authenticate(r.Context(), sessionToken(r))
	if err != nil {
		writeJSON(s.log(r), w, http.StatusOK, meResponse{User: nil})
		return
	}
	writeJSON(s.log(r), w, http.StatusOK, meResponse{User: &userDTO{
		ID:          sess.ID,
		Username:    sess.Username,
		DisplayName: sess.DisplayName,
	}})
}

// adminUnavailable reports (and writes) a 500 when the admin surface is not
// wired. It keeps a nil dependency from panicking.
func (s *server) adminUnavailable(w http.ResponseWriter, r *http.Request) bool {
	if s.adminReady() {
		return false
	}
	internalError(w, s.log(r), errors.New("admin surface not wired"))
	return true
}
