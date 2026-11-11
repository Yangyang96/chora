// Package runtimeenv validates explicitly selected native network settings.
// It never reads ambient environment variables or changes machine settings.
package runtimeenv

import (
	"crypto/sha256"
	"encoding/json"
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
	if err != nil || !strings.Contains(string(data), "-----BEGIN CERTIFICATE-----") || strings.Contains(string(data), "PRIVATE KEY") {
		return [32]byte{}, ErrConfiguration
	}
	return sha256.Sum256(data), nil
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
