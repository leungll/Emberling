// Package server is the HTTP layer of the token service. It depends only on the exported
// API of the auth package and never inspects a token itself.
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"example.com/tokenservice/internal/auth"
)

// maxBodyBytes bounds every request body.
const maxBodyBytes = 16 << 10

// Handler serves POST /token and POST /verify.
type Handler struct {
	issuer *auth.Issuer
	now    func() time.Time
}

// New returns a Handler issuing and verifying through issuer. now supplies the issue
// time; nil means the wall clock.
func New(issuer *auth.Issuer, now func() time.Time) *Handler {
	if now == nil {
		now = time.Now
	}
	return &Handler{issuer: issuer, now: now}
}

// ServeHTTP routes the two endpoints.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch r.URL.Path {
	case "/token":
		h.issue(w, r)
	case "/verify":
		h.verify(w, r)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

type issueRequest struct {
	Subject string `json:"subject"`
}

type tokenResponse struct {
	Token string `json:"token"`
}

func (h *Handler) issue(w http.ResponseWriter, r *http.Request) {
	var req issueRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil || req.Subject == "" {
		writeError(w, http.StatusBadRequest, "body must be a JSON object with a subject")
		return
	}
	token, err := h.issuer.Issue(req.Subject, h.now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token could not be issued")
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse{Token: token})
}

type verifyRequest struct {
	Token string `json:"token"`
}

type identityResponse struct {
	Subject   string    `json:"subject"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil || req.Token == "" {
		writeError(w, http.StatusBadRequest, "body must be a JSON object with a token")
		return
	}
	identity, err := h.issuer.Verify(req.Token)
	switch {
	case errors.Is(err, auth.ErrExpired):
		writeError(w, http.StatusUnauthorized, "token has expired")
	case errors.Is(err, auth.ErrAudience):
		writeError(w, http.StatusForbidden, "token is not for this service")
	case err != nil:
		writeError(w, http.StatusUnauthorized, "token is invalid")
	default:
		writeJSON(w, http.StatusOK, identityResponse{Subject: identity.Subject, ExpiresAt: identity.ExpiresAt})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
