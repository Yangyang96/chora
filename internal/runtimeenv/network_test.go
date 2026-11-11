package runtimeenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkSelectionIsExplicitImmutableAndCredentialFree(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://ambient.invalid:1234")
	empty, err := New(nil)
	if err != nil || len(empty.Values()) != 0 {
		t.Fatal("ambient environment inherited")
	}
	selected := map[string]string{"HTTPS_PROXY": "http://127.0.0.1:1234", "https_proxy": "http://127.0.0.1:1234", "NO_PROXY": "localhost,127.0.0.1"}
	n, err := New(selected)
	if err != nil {
		t.Fatal(err)
	}
	digest := n.Digest()
	selected["HTTPS_PROXY"] = "http://changed.invalid"
	values := n.Values()
	values["HTTPS_PROXY"] = "http://changed.invalid"
	if n.Digest() != digest || n.Values()["HTTPS_PROXY"] != "http://127.0.0.1:1234" {
		t.Fatal("mutable network policy")
	}
	for _, bad := range []map[string]string{
		{"NODE_OPTIONS": "--require /tmp/untrusted"}, {"NODE_TLS_REJECT_UNAUTHORIZED": "0"}, {"OPENAI_BASE_URL": "https://other.invalid"},
		{"HTTPS_PROXY": "http://user:secret@proxy.invalid"}, {"HTTP_PROXY": "http://proxy.invalid?token=secret"}, {"ALL_PROXY": "file:///tmp/proxy"},
		{"HTTPS_PROXY": "http://a.invalid", "https_proxy": "http://b.invalid"}, {"NO_PROXY": "\n*"}, {"SSL_CERT_DIR": "/tmp"},
	} {
		if _, err := New(bad); err == nil {
			t.Fatal("accepted unsupported configuration")
		}
	}
}

func TestCertificateDriftFailsClosed(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := New(map[string]string{"SSL_CERT_FILE": path})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if n.Validate() == nil {
		t.Fatal("changed CA accepted")
	}
}
