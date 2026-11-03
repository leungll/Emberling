package tests

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/tokenservice/internal/server"
)

func postJSON(t *testing.T, handler http.Handler, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response %q is not JSON: %v", rec.Body.String(), err)
	}
	return rec.Code, decoded
}

func TestHandler_IssueThenVerify_ReturnsTheSubject(t *testing.T) {
	handler := server.New(newIssuer(t, testAudience, time.Hour), nil)
	status, issued := postJSON(t, handler, "/token", `{"subject":"alice"}`)
	if status != http.StatusOK || issued["token"] == "" {
		t.Fatalf("POST /token = %d %v, want 200 with a token", status, issued)
	}
	body, _ := json.Marshal(map[string]any{"token": issued["token"]})
	status, verified := postJSON(t, handler, "/verify", string(body))
	if status != http.StatusOK || verified["subject"] != "alice" {
		t.Fatalf("POST /verify = %d %v, want 200 with subject alice", status, verified)
	}
}

func TestHandler_ExpiredToken_Answers401(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	issuing := server.New(newIssuer(t, testAudience, time.Hour), func() time.Time { return past })
	status, issued := postJSON(t, issuing, "/token", `{"subject":"alice"}`)
	if status != http.StatusOK {
		t.Fatalf("POST /token = %d, want 200", status)
	}
	verifying := server.New(newIssuer(t, testAudience, time.Hour), nil)
	body, _ := json.Marshal(map[string]any{"token": issued["token"]})
	if status, _ := postJSON(t, verifying, "/verify", string(body)); status != http.StatusUnauthorized {
		t.Fatalf("POST /verify with an expired token = %d, want 401", status)
	}
}

func TestHandler_MalformedBody_Answers400(t *testing.T) {
	handler := server.New(newIssuer(t, testAudience, time.Hour), nil)
	if status, _ := postJSON(t, handler, "/verify", `{"token":`+`""}`); status != http.StatusBadRequest {
		t.Fatalf("POST /verify without a token = %d, want 400", status)
	}
}
