//go:build chora_e2e

package localweb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Yangyang96/chora/internal/baselinebundle"
)

// The browser fixture has its own fixed compile-time identity. It is never
// present in production builds and never trusts a runtime-provided digest.
const productBaselineDigest = "d57937448b9509ca2ad3c65caaab6a46287202078143af0111889a2028a0c68c"
const e2eVerificationPatch = "diff --git a/internal/domain/task.go b/internal/domain/task.go\n--- a/internal/domain/task.go\n+++ b/internal/domain/task.go\n@@ -1,3 +1,3 @@\n package domain\n \n-// Browser fixture baseline.\n+// Browser fixture reviewed change.\n"

func prepareE2EBaseline(server *Server) error {
	if server.verifier == nil {
		return nil
	}
	root := filepath.Join(server.runtimeRoot, "e2e-baseline")
	path := filepath.Join(root, "internal", "domain", "task.go")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("package domain\n\n// Browser fixture baseline.\n"), 0444); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	entry := baselinebundle.Entry{Path: "internal/domain/task.go", SHA256: "a9c49837be5a9601a87f7d929aea9f43ffeb9fd47d2b25edefc0528925581989", Mode: "0444"}
	manifest := baselinebundle.Manifest{SchemaVersion: "chora.source-baseline.v6", SourceRevision: "public-e2e-fixture", TaskID: "browser-fixture", ContextSnapshotID: "browser-context", ContextSnapshotDigest: strings.Repeat("a", 64), EntryCount: 1, AggregateSHA256: productBaselineDigest, Entries: []baselinebundle.Entry{entry}}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err = baselinebundle.Verify(root, encoded); err != nil {
		return err
	}
	manifestPath := filepath.Join(server.runtimeRoot, "e2e-baseline-manifest.json")
	if err = os.WriteFile(manifestPath, encoded, 0600); err != nil {
		return err
	}
	server.verifier.baselineRoot = root
	server.verifier.baselineManifest = manifestPath
	return nil
}
