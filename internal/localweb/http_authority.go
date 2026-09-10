package localweb

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxLocalJSONBody = 1 << 20 // 1 MiB, including whitespace.

// localHTTPAuthority wraps the entire mux, so new routes inherit admission
// before routing, repository inspection, provider calls, or durable mutations.
// Local API clients may omit browser headers; this is not authentication against
// other local processes. Proxy-supplied forwarding headers grant no authority.
func localHTTPAuthority(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localAuthorityHost(r.Host) || !sameLocalOrigin(r) {
			writeError(w, http.StatusForbidden, errors.New("request requires a local same-origin Chora address"))
			return
		}
		if values := r.Header.Values("Sec-Fetch-Site"); len(values) > 1 || (len(values) == 1 && values[0] != "same-origin" && values[0] != "none") {
			writeError(w, http.StatusForbidden, errors.New("cross-site Chora request denied"))
			return
		}
		if !admitLocalJSON(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func localAuthorityHost(authority string) bool {
	host := authority
	if strings.Contains(authority, ":") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			// An IPv6 literal without an explicit port must still be bracketed.
			if !strings.HasPrefix(authority, "[") || !strings.HasSuffix(authority, "]") {
				return false
			}
			host = authority[1 : len(authority)-1]
			return net.ParseIP(host).IsLoopback()
		}
		p, err := strconv.Atoi(port)
		if err != nil || p < 1 || p > 65535 || strconv.Itoa(p) != port {
			return false
		}
	}
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func sameLocalOrigin(r *http.Request) bool {
	values := r.Header.Values("Origin")
	if len(values) == 0 {
		return true // Documented non-browser local API access.
	}
	if len(values) != 1 {
		return false
	}
	origin, err := url.Parse(values[0])
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && origin.Scheme == scheme && origin.Opaque == "" && origin.User == nil &&
		values[0] == scheme+"://"+origin.Host &&
		strings.EqualFold(origin.Host, r.Host)
}

func admitLocalJSON(w http.ResponseWriter, r *http.Request) bool {
	// Empty optional command bodies remain supported with a JSON media type.
	// Unsafe methods always require it, including origin-less local API calls.
	media := r.Header.Values("Content-Type")
	if len(media) == 0 && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("application/json request required"))
		return false
	}
	if len(media) > 0 {
		typeName, params, err := mime.ParseMediaType(media[0])
		if len(media) != 1 || err != nil || typeName != "application/json" ||
			(params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
			writeError(w, http.StatusUnsupportedMediaType, errors.New("application/json request required"))
			return false
		}
	}
	if r.Header.Get("Content-Encoding") != "" {
		writeError(w, http.StatusUnsupportedMediaType, errors.New("encoded request bodies are unsupported"))
		return false
	}
	if r.Body == nil {
		return true
	}
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLocalJSONBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, errors.New("JSON body exceeds 1 MiB"))
		} else {
			writeError(w, http.StatusBadRequest, errors.New("could not read JSON body"))
		}
		return false
	}
	if len(body) > 0 {
		if len(media) == 0 {
			writeError(w, http.StatusUnsupportedMediaType, errors.New("application/json request required"))
			return false
		}
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(body) {
			writeError(w, http.StatusBadRequest, errors.New("one JSON object required"))
			return false
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return true
}
