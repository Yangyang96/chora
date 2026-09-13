// Package isolatedproxy loads explicitly selected, local-only container proxy settings.
package isolatedproxy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const Filename = "isolated-proxy.json"

var ErrConfig = errors.New("invalid isolated-proxy.json; use an owner-private regular file with HTTP/HTTPS proxy URLs, no credentials, and explicit ports")

type Config struct {
	HTTPProxy  string `json:"httpProxy,omitempty"`
	HTTPSProxy string `json:"httpsProxy,omitempty"`
	NoProxy    string `json:"noProxy,omitempty"`
}

func (c Config) Validate() error {
	for _, value := range []string{c.HTTPProxy, c.HTTPSProxy} {
		if value == "" {
			continue
		}
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(value, "\r\n\t ") {
			return ErrConfig
		}
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return ErrConfig
		}
		host := strings.ToLower(u.Hostname())
		ip := net.ParseIP(host)
		if host == "localhost" || strings.HasSuffix(host, ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
			return ErrConfig
		}
	}
	for _, r := range c.NoProxy {
		if r < 32 || r > 126 {
			return ErrConfig
		}
	}
	if len(c.NoProxy) > 4096 || strings.ContainsAny(c.NoProxy, "\r\n\t") {
		return ErrConfig
	}
	for _, host := range strings.Split(c.NoProxy, ",") {
		if host == "" {
			continue
		}
		bypass := strings.Trim(strings.ToLower(strings.Split(host, ":")[0]), ".")
		if strings.Contains(host, "*") || (bypass != "" && (bypass == "api.deepseek.com" || strings.HasSuffix("api.deepseek.com", "."+bypass))) {
			return ErrConfig
		}
		if host != strings.TrimSpace(host) || strings.ContainsAny(host, " /@?#=\\") {
			return ErrConfig
		}
	}
	if !c.Enabled() && c.NoProxy != "" {
		return ErrConfig
	}
	return nil
}
func (c Config) Enabled() bool { return c.HTTPProxy != "" || c.HTTPSProxy != "" }
func (c Config) Digest() string {
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Environment declares both cases so image defaults cannot override selection.
func (c Config) Environment() map[string]string {
	if !c.Enabled() {
		return nil
	}
	https := c.HTTPSProxy
	if https == "" {
		https = c.HTTPProxy
	}
	return map[string]string{"HTTP_PROXY": c.HTTPProxy, "http_proxy": c.HTTPProxy, "HTTPS_PROXY": https, "https_proxy": https, "NO_PROXY": c.NoProxy, "no_proxy": c.NoProxy, "ALL_PROXY": "", "all_proxy": ""}
}
func (c Config) Redact(text string) string {
	values := c.PrivateValues()
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		text = strings.ReplaceAll(text, string(value), "<proxy>")
	}
	return text
}

type redactedError struct {
	message string
	cause   error
}

func (e redactedError) Error() string { return e.message }
func (e redactedError) Unwrap() error { return e.cause }
func (c Config) RedactError(err error) error {
	if err == nil {
		return nil
	}
	message := c.Redact(err.Error())
	if message == err.Error() {
		return err
	}
	return redactedError{message, err}
}

func Load(dataRoot string) (Config, error) {
	path := filepath.Join(dataRoot, Filename)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
		return Config{}, ErrConfig
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Getuid() {
		return Config{}, ErrConfig
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Config{}, ErrConfig
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) || actual.Mode() != info.Mode() {
		return Config{}, ErrConfig
	}
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(data) > 8192 {
		return Config{}, ErrConfig
	}
	// Duplicate keys can make a reviewed file disagree with its effective value.
	keys := json.NewDecoder(bytes.NewReader(data))
	if token, err := keys.Token(); err != nil || token != json.Delim('{') {
		return Config{}, ErrConfig
	}
	seen := map[string]bool{}
	for keys.More() {
		token, err := keys.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return Config{}, ErrConfig
		}
		seen[key] = true
		var value json.RawMessage
		if keys.Decode(&value) != nil {
			return Config{}, ErrConfig
		}
	}
	var document struct {
		Schema string `json:"schema"`
		Config
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) || d.Decode(&document) != nil || d.Decode(new(any)) != io.EOF || document.Schema != "chora.isolated-proxy.v1" || document.Config.Validate() != nil || !document.Config.Enabled() {
		return Config{}, ErrConfig
	}
	return document.Config, nil
}

// PrivateValues are scrubbed from captured runtime streams.
func (c Config) PrivateValues() [][]byte {
	var values [][]byte
	for _, value := range []string{c.HTTPProxy, c.HTTPSProxy} {
		if value == "" {
			continue
		}
		values = append(values, []byte(value))
		if u, err := url.Parse(value); err == nil {
			values = append(values, []byte(u.Host), []byte(u.Hostname()))
		}
	}
	if c.NoProxy != "" {
		values = append(values, []byte(c.NoProxy))
	}
	for _, token := range strings.Split(c.NoProxy, ",") {
		if token != "" {
			values = append(values, []byte(token))
			host := strings.Trim(strings.Split(token, ":")[0], ".")
			if host != "" {
				values = append(values, []byte(host))
			}
		}
	}
	return values
}
