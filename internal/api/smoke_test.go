// Package api_test runs HTTP-level smoke tests using net/http/httptest.
// These tests do NOT require a PostgreSQL database — they verify:
//   - Gin router routing and middleware wiring
//   - Request validation error responses (400)
//   - JWT auth middleware (401 without token, 401 with bad token)
//   - Response format consistency (success/error envelope)
//   - CORS preflight handling
package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evetabi/prediction/internal/api"
	"github.com/evetabi/prediction/internal/config"
	"github.com/evetabi/prediction/internal/service"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

func testCfg() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Env:  "development",
			Port: "8080",
		},
		JWT: config.JWTConfig{
			AccessSecret:  "test-access-secret-abcdefghijklmnop",
			RefreshSecret: "test-refresh-secret-abcdefghijklmnop",
			AccessTTL:     15 * time.Minute,
			RefreshTTL:    30 * 24 * time.Hour,
		},
		Wallet: config.WalletConfig{
			CommissionRate:   0.03,
			CashoutFeeRate:   0.05,
			MinWithdraw:      10,
			MaxDailyWithdraw: 50000,
		},
	}
}

// buildTestRouter creates a Gin engine with a real AuthService (no DB needed
// for token parsing) and nil for everything that requires a DB.
func buildTestRouter(t *testing.T) http.Handler {
	t.Helper()
	cfg := testCfg()
	// NewAuthService with nil DB works for ParseAccessToken (secret-only op)
	authSvc := service.NewAuthService(nil, nil, nil, cfg)

	r := api.SetupRouter(api.RouterDeps{
		AuthSvc:    authSvc,
		MarketSvc:  nil,
		BetSvc:     nil,
		WalletRepo: nil,
		Hub:        nil,
		Cfg:        cfg,
	})
	return r
}

func do(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != "" {
		buf = bytes.NewBufferString(body)
	} else {
		buf = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.NewDecoder(rr.Body).Decode(&m); err != nil {
		t.Fatalf("response is not valid JSON: %v — body: %s", err, rr.Body.String())
	}
	return m
}

// ── /health ───────────────────────────────────────────────────────────────────

func TestHealthEndpoint(t *testing.T) {
	h := buildTestRouter(t)
	rr := do(t, h, http.MethodGet, "/health", "", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("GET /health = %d, want 200", rr.Code)
	}
}

// ── Auth endpoints — validation layer ─────────────────────────────────────────

func TestRegister_MissingFields(t *testing.T) {
	h := buildTestRouter(t)
	rr := do(t, h, http.MethodPost, "/api/auth/register", `{}`, nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST /api/auth/register empty body = %d, want 400", rr.Code)
	}
	body := decodeBody(t, rr)
	if body["success"] != false {
		t.Errorf("response.success should be false on error, got %v", body["success"])
	}
	if body["code"] == nil {
		t.Errorf("error envelope missing 'code', got: %v", body)
	}
}

func TestRegister_InvalidEmail(t *testing.T) {
	h := buildTestRouter(t)
	payload := `{"username":"testuser","email":"notanemail","password":"password123"}`
	rr := do(t, h, http.MethodPost, "/api/auth/register", payload, nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("register with invalid email = %d, want 400", rr.Code)
	}
}

func TestRegister_ShortPassword(t *testing.T) {
	h := buildTestRouter(t)
	payload := `{"username":"testuser","email":"user@example.com","password":"short"}`
	rr := do(t, h, http.MethodPost, "/api/auth/register", payload, nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("register with short password = %d, want 400", rr.Code)
	}
}

func TestLogin_MissingFields(t *testing.T) {
	h := buildTestRouter(t)
	rr := do(t, h, http.MethodPost, "/api/auth/login", `{}`, nil)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("POST /api/auth/login empty = %d, want 400", rr.Code)
	}
}

// ── JWT auth middleware (no token → 401) ──────────────────────────────────────

func TestMe_NoToken_Returns401(t *testing.T) {
	h := buildTestRouter(t)
	rr := do(t, h, http.MethodGet, "/api/me", "", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/me without token = %d, want 401", rr.Code)
	}
}

func TestMyBets_NoToken_Returns401(t *testing.T) {
	h := buildTestRouter(t)
	rr := do(t, h, http.MethodGet, "/api/bets/my", "", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/bets/my without token = %d, want 401", rr.Code)
	}
}

func TestPlaceBet_NoToken_Returns401(t *testing.T) {
	h := buildTestRouter(t)
	payload := `{"market_id":"11111111-1111-1111-1111-111111111111","direction":"UP","amount":"100.00"}`
	rr := do(t, h, http.MethodPost, "/api/bets", payload, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/bets without token = %d, want 401", rr.Code)
	}
}

func TestWalletBalance_NoToken_Returns401(t *testing.T) {
	h := buildTestRouter(t)
	rr := do(t, h, http.MethodGet, "/api/wallet/balance", "", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/wallet/balance without token = %d, want 401", rr.Code)
	}
}

func TestWalletWithdraw_NoToken_Returns401(t *testing.T) {
	h := buildTestRouter(t)
	payload := `{"amount":"100.00","iban":"TR330006100519786457841326"}`
	rr := do(t, h, http.MethodPost, "/api/wallet/withdraw", payload, nil)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/wallet/withdraw without token = %d, want 401", rr.Code)
	}
}

// ── JWT auth middleware (invalid token → 401) ─────────────────────────────────

func TestMe_InvalidToken_Returns401(t *testing.T) {
	h := buildTestRouter(t)
	rr := do(t, h, http.MethodGet, "/api/me", "", map[string]string{
		"Authorization": "Bearer not.a.valid.jwt",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/me with bad JWT = %d, want 401", rr.Code)
	}
}

func TestPlaceBet_InvalidToken_Returns401(t *testing.T) {
	h := buildTestRouter(t)
	payload := `{"market_id":"11111111-1111-1111-1111-111111111111","direction":"UP","amount":"100.00"}`
	// A well-formed JWT header+payload but wrong secret → ParseAccessToken will reject it
	fakeJWT := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" +
		".eyJzdWIiOiIxMjM0NTY3ODkwIiwicm9sZSI6InVzZXIiLCJ0eXBlIjoiYWNjZXNzIn0" +
		".BADSIG"
	rr := do(t, h, http.MethodPost, "/api/bets", payload, map[string]string{
		"Authorization": "Bearer " + fakeJWT,
	})
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/bets with invalid JWT = %d, want 401", rr.Code)
	}
}

// ── Markets public endpoints ───────────────────────────────────────────────────

func TestMarketsActive_IsPublic(t *testing.T) {
	h := buildTestRouter(t)
	// No token: should NOT be 401. Will be 500 (nil marketSvc) — that's acceptable.
	rr := do(t, h, http.MethodGet, "/api/markets/active", "", nil)
	if rr.Code == http.StatusUnauthorized {
		t.Error("GET /api/markets/active should be a public endpoint (no 401)")
	}
	t.Logf("GET /api/markets/active = %d (not 401, public route OK)", rr.Code)
}

func TestMarketsHistory_IsPublic(t *testing.T) {
	h := buildTestRouter(t)
	rr := do(t, h, http.MethodGet, "/api/markets/history", "", nil)
	if rr.Code == http.StatusUnauthorized {
		t.Error("GET /api/markets/history should be public (no 401)")
	}
}

// ── Error envelope format ─────────────────────────────────────────────────────

func TestErrorEnvelope_HasRequiredFields(t *testing.T) {
	h := buildTestRouter(t)
	rr := do(t, h, http.MethodPost, "/api/auth/register", `{}`, nil)
	body := decodeBody(t, rr)

	for _, field := range []string{"success", "error", "code"} {
		if _, ok := body[field]; !ok {
			t.Errorf("error envelope missing field %q, got: %v", field, body)
		}
	}
	if body["success"] != false {
		t.Errorf("error envelope.success = %v, want false", body["success"])
	}
}

// ── CORS headers ──────────────────────────────────────────────────────────────

func TestCORSOptionsRequest(t *testing.T) {
	h := buildTestRouter(t)
	req := httptest.NewRequest(http.MethodOptions, "/api/auth/login", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// OPTIONS should return 204 (no content) in dev mode
	if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
		t.Errorf("OPTIONS /api/auth/login = %d, want 204 or 200", rr.Code)
	}
	allow := rr.Header().Get("Access-Control-Allow-Methods")
	if !strings.Contains(allow, "POST") {
		t.Errorf("Access-Control-Allow-Methods missing POST, got %q", allow)
	}
}

func TestCORSAllowOrigin_Dev(t *testing.T) {
	h := buildTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	// In dev mode, CORS origin should be wildcard
	origin := rr.Header().Get("Access-Control-Allow-Origin")
	if origin != "*" {
		t.Errorf("Dev CORS origin = %q, want *", origin)
	}
}
