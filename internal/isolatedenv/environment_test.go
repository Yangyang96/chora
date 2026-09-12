package isolatedenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/pi"
)

func TestBuildContextContainsOnlyFrozenPublicRuntimeInputs(t *testing.T) {
	source, stage := t.TempDir(), t.TempDir()
	observer := filepath.Join(source, "internal", "agent", "pi", "resource_check_observer.mjs")
	if err := os.MkdirAll(filepath.Dir(observer), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(observer, []byte("observer"), 0o644); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(source, "helper")
	if err := os.WriteFile(helper, []byte("helper"), 0755); err != nil {
		t.Fatal(err)
	}
	oldDigest := observerDigest
	observerDigest = func() string { return digestBytes([]byte("observer")) }
	defer func() { observerDigest = oldDigest }()
	tarball := filepath.Join(source, "qualified-package.tgz")
	if err := os.WriteFile(tarball, []byte("package"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeBuildContext(source, stage, helper, tarball); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Dockerfile", "package.json", "package-lock.json", "qualified-package.tgz", "resource_check_observer.mjs", "chora"} {
		if _, err := os.Lstat(filepath.Join(stage, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	b, err := os.ReadFile(filepath.Join(stage, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if strings.Contains(text, source) || strings.Contains(text, "auth") || strings.Contains(text, "--pull") ||
		!strings.Contains(text, "COPY qualified-package.tgz /opt/qualified-package.tgz") ||
		!strings.Contains(text, `apt-get install --yes --no-install-recommends git=1:2.39.5-0+deb12u3`) ||
		!strings.Contains(text, "npm ci --ignore-scripts --omit=dev") ||
		!strings.Contains(text, "chmod 0555 /usr/local/bin/chora") ||
		!strings.Contains(text, "chmod 0444 "+pi.IsolatedObserverPath) ||
		!strings.Contains(text, "git --version") || !strings.Contains(text, "node --version") ||
		!strings.Contains(text, "dist/bundle/cli.js") {
		t.Fatalf("unsafe Dockerfile: %s", text)
	}
}

func TestPlatformRestrictionIsExplicit(t *testing.T) {
	if err := validatePlatform("darwin", "arm64"); err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]string{{"linux", "arm64"}, {"darwin", "amd64"}} {
		if validatePlatform(p[0], p[1]) == nil {
			t.Fatalf("accepted %v", p)
		}
	}
}
