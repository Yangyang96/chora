package isolatedproxy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validDocument = `{"schema":"chora.isolated-proxy.v1","httpProxy":"http://proxy.example:8080","noProxy":"localhost,127.0.0.1"}`

func TestLoadExplicitPrivateProxy(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, Filename)
	absent, err := Load(root)
	if err != nil || absent.Enabled() {
		t.Fatal("absent config must be direct")
	}
	if err := os.WriteFile(path, []byte(validDocument), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(root)
	if err != nil || !c.Enabled() {
		t.Fatal(err)
	}
	env := c.Environment()
	if env["https_proxy"] != c.HTTPProxy || env["HTTPS_PROXY"] != c.HTTPProxy || env["ALL_PROXY"] != "" {
		t.Fatal("proxy fallback or case mismatch")
	}
	for _, mutation := range []string{"duplicate", "credentials", "unknown", "trailing", "null", "empty", "bad-schema", "size", "mode", "symlink"} {
		t.Run(mutation, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, Filename)
			data := validDocument
			mode := os.FileMode(0600)
			switch mutation {
			case "duplicate":
				data = strings.Replace(data, `"httpProxy":`, `"httpProxy":"http://other.example:8080","httpProxy":`, 1)
			case "credentials":
				data = strings.Replace(data, "proxy.example", "user:password@proxy.example", 1)
			case "unknown":
				data = strings.Replace(data, "httpProxy", "proxy", 1)
			case "trailing":
				data += "{}"
			case "null":
				data = "null"
			case "empty":
				data = `{"schema":"chora.isolated-proxy.v1"}`
			case "bad-schema":
				data = strings.Replace(data, ".v1", ".v2", 1)
			case "size":
				data = strings.Repeat(" ", 8193)
			case "mode":
				mode = 0644
			}
			if mutation == "symlink" {
				if err := os.Symlink(path, p); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(p, []byte(data), mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "proxy.example") {
				t.Fatalf("unsafe config not rejected privately: %v", err)
			}
		})
	}
}
func TestValidateRejectsUnusableRoutesAndModelBypass(t *testing.T) {
	for _, route := range []string{"socks5://proxy.example:1080", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080", "http://0.0.0.0:8080", "http://proxy.example", "http://proxy.example:0", "http://proxy.example:99999", "http://proxy.example:8080/path", "http://proxy.example:8080?secret=yes", "http://proxy.example:8080#fragment"} {
		if (Config{HTTPProxy: route}).Validate() == nil {
			t.Fatalf("accepted %s", route)
		}
	}
	for _, bypass := range []string{"*", "*.deepseek.com", "api.deepseek.com.", "DeepSeek.COM:443", "api.deepseek.com", ".deepseek.com", "com", "localhost\nHTTPS_PROXY=bad"} {
		if (Config{HTTPProxy: "http://proxy.example:8080", NoProxy: bypass}).Validate() == nil {
			t.Fatalf("accepted bypass %q", bypass)
		}
	}
	c := Config{HTTPSProxy: "https://proxy.example:8443"}
	if c.Validate() != nil {
		t.Fatal("HTTPS proxy rejected")
	}
	if strings.Contains(c.Redact("https://proxy.example:8443 failed at proxy.example:8443"), "proxy.example") {
		t.Fatal("endpoint disclosed")
	}
}

func TestProxyBypassListIsPrivateAndErrorsKeepIdentity(t *testing.T) {
	c := Config{HTTPProxy: "http://proxy.example:8080", NoProxy: "private.example,other.example:8443"}
	for _, value := range []string{c.NoProxy, "private.example", "other.example:8443", "other.example"} {
		if strings.Contains(c.Redact("failure "+value), value) {
			t.Fatal("bypass leaked")
		}
	}
	cause := errors.New("private.example failed")
	safe := c.RedactError(cause)
	if !errors.Is(safe, cause) || strings.Contains(safe.Error(), "private.example") {
		t.Fatal("redacted error lost identity or leaked")
	}
}
