package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkbenchPersistentDataRoot(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "default"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", root)
			source := filepath.Join(root, "source")
			if err := os.MkdirAll(filepath.Join(source, "web", "dist"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "web", "dist", "index.html"), []byte("<html></html>"), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, ".chora", "data")
			args := []string{"--source", source}
			if explicit {
				target = filepath.Join(root, "custom")
				args = append(args, "--data", target)
			}
			var out, diagnostic bytes.Buffer
			listened := false
			code := runWorkbenchWithListen(args, &out, &diagnostic, func(*http.Server) error { listened = true; return nil })
			if code != 0 || !listened {
				t.Fatalf("code=%d stderr=%s", code, diagnostic.String())
			}
			if _, err := os.Stat(filepath.Join(target, "chora.db")); err != nil {
				t.Fatal(err)
			}
			if explicit {
				if _, err := os.Stat(filepath.Join(root, ".chora")); !os.IsNotExist(err) {
					t.Fatalf("override touched default directory: %v", err)
				}
			}
		})
	}
}
