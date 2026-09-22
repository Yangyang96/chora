package dockersupervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestExactMountsDockerDesktopBindIdentity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, actual, expected, kind string
		rw, want                     bool
	}{
		{"exact bind", source, source, "bind", false, true},
		{"desktop VM path", "/host_mnt" + canonical, source, "bind", false, runtime.GOOS == "darwin"},
		{"desktop canonical alias", "/host_mnt" + canonical, alias, "bind", false, runtime.GOOS == "darwin"},
		{"different source", "/host_mnt" + canonical + "-other", source, "bind", false, false},
		{"writable source", "/host_mnt" + canonical, source, "bind", true, false},
		{"wrong type", "/host_mnt" + canonical, source, "volume", false, false},
		{"missing source", "/host_mnt" + canonical + "/missing", source + "/missing", "bind", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"Mounts": []map[string]any{{"Type": tc.kind, "Source": tc.actual, "Name": tc.actual, "Destination": "/input/context", "RW": tc.rw}}})
			var container effectiveContainer
			if err := json.Unmarshal(body, &container); err != nil {
				t.Fatal(err)
			}
			expected := map[string]effectiveMount{"/input/context": {Source: tc.expected, RW: false}}
			if got := exactMounts(container, expected); got != tc.want {
				t.Fatalf("exactMounts=%v want %v", got, tc.want)
			}
			container.Mounts = append(container.Mounts, container.Mounts[0])
			if exactMounts(container, expected) {
				t.Fatal("accepted extra mount")
			}
		})
	}
}
