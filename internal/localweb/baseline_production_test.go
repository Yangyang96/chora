//go:build !chora_e2e

package localweb

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/baselinebundle"
)

func TestProductionBaselineCannotBeReboundThroughRuntimeAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte("different baseline\n")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), content, 0444); err != nil {
		t.Fatal(err)
	}
	entry := baselinebundle.Entry{Path: "file.txt", SHA256: fmt.Sprintf("%x", sha256.Sum256(content)), Mode: "0444"}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\n", entry.Path, entry.SHA256, entry.Mode)))
	manifest := baselinebundle.Manifest{SchemaVersion: "chora.source-baseline.v6", SourceRevision: "test", TaskID: "test", ContextSnapshotID: "test", ContextSnapshotDigest: strings.Repeat("a", 64), EntryCount: 1, AggregateSHA256: fmt.Sprintf("%x", digest), Entries: []baselinebundle.Entry{entry}}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := baselinebundle.Verify(root, encoded); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	service := &productVerifier{baselineRoot: root, baselineManifest: path, authority: app.VerificationAuthority{BaselineDigest: digest}}
	if err := service.VerifyBaselineIdentity(); err == nil {
		t.Fatal("production accepted a runtime-rebound frozen baseline")
	}
}
