// Package runtimeenv validates explicitly selected native network settings.
// It never reads ambient environment variables or changes machine settings.
package runtimeenv

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

var ErrConfiguration = errors.New("network_configuration_unqualified")

type Network struct {
	values       map[string]string
	certificates map[string][32]byte
}

// New freezes a small allowlist. Credential-bearing proxy URLs are unsupported:
// neither secrets nor hashes of secrets belong in persisted runtime bindings.
func New(selected map[string]string) (Network, error) {
	n := Network{values: map[string]string{}, certificates: map[string][32]byte{}}
	for key, value := range selected {
		if value == "" || len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") {
			return Network{}, ErrConfiguration
		}
		switch key {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy":
			u, err := url.Parse(value)
			if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
				return Network{}, ErrConfiguration
			}
			switch u.Scheme {
			case "http", "https", "socks5", "socks5h":
			default:
				return Network{}, ErrConfiguration
			}
		case "NO_PROXY", "no_proxy":
			if strings.ContainsAny(value, "@/?#") {
				return Network{}, ErrConfiguration
			}
		case "SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS":
			sum, err := certificate(value)
			if err != nil {
				return Network{}, err
			}
			n.certificates[key] = sum
		default:
			return Network{}, ErrConfiguration
		}
		n.values[key] = value
	}
	// Avoid ambiguous proxy precedence between native HTTP implementations.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY"} {
		if upper, lower := n.values[key], n.values[strings.ToLower(key)]; upper != "" && lower != "" && upper != lower {
			return Network{}, ErrConfiguration
		}
	}
	return n, nil
}

func certificate(path string) ([32]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || !filepath.IsAbs(path) || resolved != path || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return [32]byte{}, ErrConfiguration
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4<<20 || strings.Contains(string(data), "PRIVATE KEY") || !validCertificatePEM(data) {
		return [32]byte{}, ErrConfiguration
	}
	return sha256.Sum256(data), nil
}

// validCertificatePEM checks syntax, not trust, usage or validity dates. Files
// must contain one or more PEM CERTIFICATE blocks with no PEM headers, each
// holding exactly one X.509 DER certificate. Raw DER files remain unsupported.
// Between blocks, allow ASCII space/tab/CR blank lines and whole-line # comments
// (optionally space/tab indented). Boundaries start at column zero; LF and CRLF
// and trailing space/tab on boundaries are supported. Other text is rejected.
func validCertificatePEM(data []byte) bool {
	lines := strings.Split(string(data), "\n")
	count := 0
	for i := 0; i < len(lines); i++ {
		line := strings.Trim(lines[i], " \t\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.TrimRight(lines[i], " \t\r") != "-----BEGIN CERTIFICATE-----" {
			return false
		}
		start := i
		for i++; i < len(lines); i++ {
			if strings.TrimRight(lines[i], " \t\r") == "-----END CERTIFICATE-----" {
				break
			}
			// pem.Decode can skip a malformed block and find a later valid one.
			// Reject nested/mismatched boundaries before giving it this block.
			if strings.Contains(lines[i], "-----") {
				return false
			}
		}
		if i == len(lines) {
			return false
		}
		block, rest := pem.Decode([]byte(strings.Join(lines[start:i+1], "\n") + "\n"))
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(rest) != 0 {
			return false
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return false
		}
		count++
	}
	return count > 0
}

func (n Network) Values() map[string]string {
	copy := map[string]string{}
	for k, v := range n.values {
		copy[k] = v
	}
	return copy
}

func (n Network) Validate() error {
	for key, want := range n.certificates {
		got, err := certificate(n.values[key])
		if err != nil || got != want {
			return ErrConfiguration
		}
	}
	return nil
}

func (n Network) Digest() [32]byte {
	data, _ := json.Marshal(struct {
		Values       map[string]string
		Certificates map[string][32]byte
	}{n.values, n.certificates})
	return sha256.Sum256(data)
}
