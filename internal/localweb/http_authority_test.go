package localweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestLocalHTTPAuthorityAdmission(t *testing.T) {
	tests := []struct {
		name, host, origin, site, media, body string
		status                                int
	}{
		{"same origin", "localhost:8787", "http://localhost:8787", "same-origin", "application/json", "{}", 204},
		{"local API", "127.0.0.1:8787", "", "", "application/json", "{}", 204},
		{"IPv6", "[::1]:8787", "http://[::1]:8787", "same-origin", "application/json; charset=utf-8", "{}", 204},
		{"empty optional", "localhost", "", "", "application/json", "", 204},
		{"empty unsafe without media", "localhost", "", "none", "", "", 415},
		{"arbitrary host", "evil.example:8787", "http://evil.example:8787", "same-origin", "application/json", "{}", 403},
		{"suffix", "localhost.evil.example", "", "", "application/json", "{}", 403},
		{"bad port", "localhost:bad", "", "", "application/json", "{}", 403},
		{"empty port", "localhost:", "", "", "application/json", "{}", 403},
		{"remote origin", "localhost:8787", "https://evil.example", "", "application/json", "{}", 403},
		{"port mismatch", "localhost:8787", "http://localhost:8788", "same-origin", "application/json", "{}", 403},
		{"scheme mismatch", "localhost", "https://localhost", "", "application/json", "{}", 403},
		{"alias mismatch", "localhost", "http://127.0.0.1", "", "application/json", "{}", 403},
		{"null origin", "localhost", "null", "", "application/json", "{}", 403},
		{"origin with path", "localhost", "http://localhost/", "", "application/json", "{}", 403},
		{"origin with user", "localhost", "http://user@localhost", "", "application/json", "{}", 403},
		{"cross-site no origin", "localhost", "", "cross-site", "application/json", "{}", 403},
		{"same-site", "localhost", "", "same-site", "application/json", "{}", 403},
		{"conflicting metadata", "localhost", "http://localhost", "cross-site", "application/json", "{}", 403},
		{"text JSON", "localhost", "", "", "text/plain", "{}", 415},
		{"form", "localhost", "", "", "application/x-www-form-urlencoded", "{}", 415},
		{"missing media", "localhost", "", "", "", "{}", 415},
		{"unsupported charset", "localhost", "", "", "application/json; charset=utf-16", "{}", 415},
		{"trailing object", "localhost", "", "", "application/json", "{}{}", 400},
		{"trailing garbage", "localhost", "", "", "application/json", "{}x", 400},
		{"truncated", "localhost", "", "", "application/json", "{", 400},
		{"null", "localhost", "", "", "application/json", "null", 400},
		{"array", "localhost", "", "", "application/json", "[]", 400},
		{"whitespace only", "localhost", "", "", "application/json", " ", 400},
		{"exact limit", "localhost", "", "", "application/json", "{}" + strings.Repeat(" ", maxLocalJSONBody-2), 204},
		{"oversized whitespace", "localhost", "", "", "application/json", "{}" + strings.Repeat(" ", maxLocalJSONBody-1), 413},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			handler := localHTTPAuthority(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }))
			r := httptest.NewRequest(http.MethodPost, "/api/future-scm", strings.NewReader(tc.body))
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			if tc.media != "" {
				r.Header.Set("Content-Type", tc.media)
			}
			// Unknown length proves enforcement on the stream, not just Content-Length.
			r.ContentLength = -1
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.status || (tc.status != 204 && calls != 0) {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
		})
	}
}

func TestProductHandlerRejectsBeforeAuthoritySideEffects(t *testing.T) {
	// Nil services deliberately witness non-entry: any attempt to reach the
	// service/DB panics. Test the real mux, including optional/empty-body routes.
	server := &Server{product: true}
	routes := []struct{ method, path string }{
		{"POST", "/api/projects"}, {"POST", "/api/projects/choose-directory"},
		{"POST", "/api/projects/p/open-external"}, {"DELETE", "/api/projects/p?expectedVersion=1"},
		{"POST", "/api/agent-execution/trusted-local-acknowledgements"},
		{"POST", "/api/tasks/t/runs"}, {"POST", "/api/runs/r/review"}, {"POST", "/api/runs/r/apply"},
		{"POST", "/api/readiness/refresh"}, {"POST", "/api/product-installation/upgrade"},
		{"POST", "/api/projects/p/commit"}, {"POST", "/api/projects/p/push"},
		{"GET", "/api/projects"}, {"GET", "/api/runs/r/events"},
	}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			for _, attack := range []struct {
				name, host, origin, media, body string
				status                          int
			}{
				{"cross origin", "localhost", "https://evil.example", "application/json", "{}", 403},
				{"rebound host", "evil.example", "", "application/json", "{}", 403},
				{"text", "localhost", "", "text/plain", "{}", 415},
				{"trailing", "localhost", "", "application/json", "{}{}", 400},
				{"oversized", "localhost", "", "application/json", "{}" + strings.Repeat(" ", maxLocalJSONBody), 413},
			} {
				t.Run(attack.name, func(t *testing.T) {
					r := httptest.NewRequest(route.method, route.path, strings.NewReader(attack.body))
					r.Host = attack.host
					r.Header.Set("Content-Type", attack.media)
					if attack.origin != "" {
						r.Header.Set("Origin", attack.origin)
					}
					w := httptest.NewRecorder()
					server.Handler().ServeHTTP(w, r)
					if w.Code != attack.status {
						t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
					}
				})
			}
		})
	}
}

func TestHTTPAuthorityRejectedAcknowledgementDoesNotPersist(t *testing.T) {
	server := newReadinessTestServer(t)
	body := `{"policyVersion":"` + domain.TrustedLocalDisclosurePolicy + `"}`
	for _, suffix := range []string{"", "{}", strings.Repeat(" ", maxLocalJSONBody)} {
		r := httptest.NewRequest("POST", "http://localhost/api/agent-execution/trusted-local-acknowledgements", strings.NewReader(body+suffix))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "http-authority-ack")
		if suffix == "" {
			r.Header.Set("Origin", "https://evil.example")
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code < 400 {
			t.Fatalf("accepted: %s", w.Body.String())
		}
		if _, err := server.store.Reader().GetTrustedLocalAcknowledgement(context.Background(), localActor, domain.TrustedLocalDisclosurePolicy); err != storecontract.ErrNotFound {
			t.Fatalf("rejected request persisted: %v", err)
		}
	}
	// The same idempotency key remains usable by a legitimate same-origin request.
	requestJSONWithHeaders(t, server.Handler(), "POST", "/api/agent-execution/trusted-local-acknowledgements", map[string]string{"policyVersion": domain.TrustedLocalDisclosurePolicy}, map[string]string{"Origin": "http://localhost", "Sec-Fetch-Site": "same-origin", "Idempotency-Key": "http-authority-ack"}, http.StatusOK, nil)
}

func TestHTTPAuthorityAmbiguousHeadersAndReadRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		header http.Header
		status int
	}{
		{"read navigation", "GET", http.Header{"Sec-Fetch-Site": {"none"}}, 204},
		{"cross-site read", "GET", http.Header{"Sec-Fetch-Site": {"cross-site"}}, 403},
		{"empty origin", "POST", http.Header{"Origin": {""}}, 403},
		{"duplicate origins", "POST", http.Header{"Origin": {"http://localhost", "http://localhost"}}, 403},
		{"origin list", "POST", http.Header{"Origin": {"http://localhost http://localhost"}}, 403},
		{"empty fragment", "POST", http.Header{"Origin": {"http://localhost#"}}, 403},
		{"duplicate fetch metadata", "POST", http.Header{"Sec-Fetch-Site": {"same-origin", "cross-site"}}, 403},
		{"duplicate media", "POST", http.Header{"Content-Type": {"application/json", "text/plain"}}, 415},
		{"encoded body", "POST", http.Header{"Content-Encoding": {"gzip"}}, 415},
		{"forwarded authority", "POST", http.Header{"Origin": {"https://remote.example"}, "X-Forwarded-Host": {"remote.example"}, "X-Forwarded-Proto": {"https"}}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := localHTTPAuthority(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }))
			r := httptest.NewRequest(tc.method, "http://localhost/", nil)
			r.Header.Set("Content-Type", "application/json")
			for k, v := range tc.header {
				r.Header[k] = v
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status || (tc.status != 204 && calls != 0) {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
		})
	}
}
