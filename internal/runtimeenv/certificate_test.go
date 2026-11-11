package runtimeenv

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCertificateRejectsInvalidFixture(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(map[string]string{"SSL_CERT_FILE": path}); err != ErrConfiguration {
		t.Fatalf("invalid certificate: got %v, want ErrConfiguration", err)
	}
}

// Keys exist only in memory; no real credentials or machine trust stores are used.
// Deliberately expired and not a CA: this preflight checks format only.
func syntheticCertificate(t *testing.T, serial int64) ([]byte, []byte) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		NotBefore:    time.Unix(0, 0), NotAfter: time.Unix(1, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), der
}

func certificatePath(t *testing.T, data []byte) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCertificateFormats(t *testing.T) {
	one, der := syntheticCertificate(t, 1)
	two, _ := syntheticCertificate(t, 2)
	valid := string(one)
	badDER := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not DER")}))
	badBase64 := "-----BEGIN CERTIFICATE-----\n!bad!\n-----END CERTIFICATE-----\n"
	truncated := "-----BEGIN CERTIFICATE-----\nYWJj\n"
	private := "-----BEGIN PRIVATE KEY-----\nbm90IGEga2V5\n-----END PRIVATE KEY-----\n"
	other := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("synthetic")}))
	cases := []struct {
		name, data string
		valid      bool
	}{
		{"single", valid, true},
		{"multiple", valid + string(two), true},
		{"comments_and_whitespace", " \t\r\n# bundle\n\t# comment\n" + valid + "\n# between\n" + string(two) + "# end", true},
		{"crlf", strings.ReplaceAll(valid, "\n", "\r\n"), true},
		{"no_final_newline", strings.TrimSuffix(valid, "\n"), true},
		{"boundary_trailing_space", strings.ReplaceAll(valid, "-----\n", "----- \t\n"), true},
		{"empty_file", "", false},
		{"only_comments", "# no certificates\n \t\r\n", false},
		{"empty_block", "-----BEGIN CERTIFICATE-----\n-----END CERTIFICATE-----\n", false},
		{"invalid_base64", badBase64, false},
		{"invalid_der", badDER, false},
		{"truncated_der", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der[:len(der)-1]})), false},
		{"der_trailing_bytes", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: append(append([]byte{}, der...), 0)})), false},
		{"two_der_in_one_block", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: append(append([]byte{}, der...), der...)})), false},
		{"raw_der", string(der), false},
		{"truncated_pem", truncated, false},
		{"missing_begin", strings.Replace(valid, "-----BEGIN CERTIFICATE-----\n", "", 1), false},
		{"mismatched_end", strings.Replace(valid, "END CERTIFICATE", "END PUBLIC KEY", 1), false},
		{"pem_headers", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"Comment": "unsupported"}, Bytes: der})), false},
		{"private_key", private, false},
		{"private_before", private + valid, false},
		{"private_after", valid + private, false},
		{"other_before", other + valid, false},
		{"other_after", valid + other, false},
		{"bad_der_before", badDER + valid, false},
		{"bad_der_after", valid + badDER, false},
		{"bad_pem_before", badBase64 + valid, false},
		{"bad_pem_after", valid + badBase64, false},
		{"truncated_before", truncated + valid, false},
		{"truncated_after", valid + truncated, false},
		{"junk_before", "unrecognized text\n" + valid, false},
		{"junk_after", valid + "unrecognized text", false},
		{"inline_comment", strings.Replace(valid, "-----END CERTIFICATE-----", "-----END CERTIFICATE----- # inline", 1), false},
		{"comment_in_block", strings.Replace(valid, "-----BEGIN CERTIFICATE-----\n", "-----BEGIN CERTIFICATE-----\n# comment\n", 1), false},
		{"secret_marker_in_comment", "# PRIVATE KEY\n" + valid, false},
	}
	for _, key := range []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS"} {
		t.Run(key, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					path := certificatePath(t, []byte(tc.data))
					n, err := New(map[string]string{key: path})
					if !tc.valid {
						// Exact sentinel also proves no path, parser detail or content leaks.
						if err != ErrConfiguration {
							t.Fatalf("got %v, want ErrConfiguration", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if err := n.Validate(); err != nil {
						t.Fatal(err)
					}
					if n.Values()[key] != path {
						t.Fatal("certificate selection not preserved")
					}
				})
			}
		})
	}
}

func TestCertificatePathAndSizeConstraints(t *testing.T) {
	cert, _ := syntheticCertificate(t, 1)
	path := certificatePath(t, cert)
	link := filepath.Join(filepath.Dir(path), "link.pem")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(filepath.Dir(path), "linked-dir")
	if err := os.Symlink(filepath.Dir(path), parentLink); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"relative": relative, "symlink": link, "parent_symlink": filepath.Join(parentLink, "ca.pem"),
		"directory": filepath.Dir(path), "missing": path + ".missing",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(map[string]string{"SSL_CERT_FILE": bad}); err != ErrConfiguration {
				t.Fatalf("got %v, want ErrConfiguration", err)
			}
		})
	}
	padded := append(append([]byte{}, cert...), []byte(strings.Repeat(" ", (4<<20)-len(cert)))...)
	atLimit := certificatePath(t, padded)
	if _, err := New(map[string]string{"SSL_CERT_FILE": atLimit}); err != nil {
		t.Fatalf("size limit rejected: %v", err)
	}
	overLimit := certificatePath(t, append(padded, ' '))
	if _, err := New(map[string]string{"SSL_CERT_FILE": overLimit}); err != ErrConfiguration {
		t.Fatalf("oversize: got %v", err)
	}
}

func TestCertificateValidReplacementStillDrifts(t *testing.T) {
	one, _ := syntheticCertificate(t, 1)
	two, _ := syntheticCertificate(t, 2)
	for name, replacement := range map[string][]byte{"different_certificate": two, "only_comment": append(append([]byte{}, one...), []byte("# changed\n")...)} {
		t.Run(name, func(t *testing.T) {
			path := certificatePath(t, one)
			selected := map[string]string{"NODE_EXTRA_CA_CERTS": path}
			n, err := New(selected)
			if err != nil {
				t.Fatal(err)
			}
			digest := n.Digest()
			selected["NODE_EXTRA_CA_CERTS"] = "changed"
			values := n.Values()
			values["NODE_EXTRA_CA_CERTS"] = "changed"
			if err := os.WriteFile(path, replacement, 0600); err != nil {
				t.Fatal(err)
			}
			fresh, err := New(map[string]string{"NODE_EXTRA_CA_CERTS": path})
			if err != nil {
				t.Fatal(err)
			}
			if n.Validate() != ErrConfiguration {
				t.Fatal("accepted certificate drift")
			}
			if n.Digest() != digest || fresh.Digest() == digest {
				t.Fatal("certificate digest not frozen")
			}
		})
	}
}
