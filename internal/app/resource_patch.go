package app

import (
	"crypto/sha256"
	"encoding/json"
	"sort"

	"github.com/Yangyang96/chora/internal/domain"
)

// BuildResourceReviewablePatch derives one repository's review projection from
// the collected changed paths. The repository snapshot, rather than a caller's
// enumeration of the whole repository, is the frozen scope authority.
func BuildResourceReviewablePatch(resource domain.TaskRepositoryResource, changed []string, raw []byte, digest [32]byte) (ReviewablePatch, error) {
	if len(changed) == 0 || len(raw) == 0 || digest == ([32]byte{}) || sha256.Sum256(raw) != digest {
		return ReviewablePatch{}, reviewEvidenceError("resource Patch bytes do not match the frozen digest")
	}
	if _, err := domain.ParseRepositoryID(resource.RepoID); err != nil || resource.Role != "write" {
		return ReviewablePatch{}, reviewEvidenceError("resource Patch does not bind a writable repository")
	}

	declared := make([]string, 0, len(changed))
	seen := make(map[string]struct{}, len(changed))
	for _, relative := range changed {
		if !resource.Scope.Allows(relative) {
			return ReviewablePatch{}, reviewEvidenceError("resource Patch changes a file outside its frozen scope: " + relative)
		}
		if _, exists := seen[relative]; exists {
			return ReviewablePatch{}, reviewEvidenceError("resource Patch repeats a changed path: " + relative)
		}
		seen[relative] = struct{}{}
		declared = append(declared, relative)
	}
	sort.Strings(declared)
	files, err := parseUnifiedPatch(raw, declared, nil)
	if err != nil {
		return ReviewablePatch{}, err
	}
	if len(files) != len(declared) {
		return ReviewablePatch{}, reviewEvidenceError("resource Patch does not contain every collected changed path")
	}
	for _, file := range files {
		if _, exists := seen[file.Path]; !exists {
			return ReviewablePatch{}, reviewEvidenceError("resource Patch paths differ from the collected changes")
		}
	}

	return ReviewablePatch{
		Raw:                 append([]byte(nil), raw...),
		PatchDigest:         digest,
		DeclaredFilesDigest: resourceReviewScopeDigest(resource),
		Files:               files,
	}, nil
}

func resourceReviewScopeDigest(resource domain.TaskRepositoryResource) [32]byte {
	type frozenScope struct {
		SchemaVersion      string                     `json:"schemaVersion"`
		RepoID             string                     `json:"repoId"`
		Role               string                     `json:"role"`
		BaseCommit         string                     `json:"baseCommit"`
		BaseTree           string                     `json:"baseTree"`
		BaseRef            string                     `json:"baseRef"`
		AssociationVersion uint64                     `json:"associationVersion"`
		Scope              domain.TaskRepositoryScope `json:"scope"`
	}
	scope := resource.Scope
	scope.WritableFiles = sortedResourceScopeStrings(scope.WritableFiles)
	scope.WritableDirectories = sortedResourceScopeStrings(scope.WritableDirectories)
	scope.ProtectedDirectories = sortedResourceScopeStrings(scope.ProtectedDirectories)
	canonical, _ := json.Marshal(frozenScope{
		SchemaVersion: "chora.resource-review-scope.v1", RepoID: resource.RepoID, Role: resource.Role,
		BaseCommit: resource.BaseCommit, BaseTree: resource.BaseTree, BaseRef: resource.BaseRef,
		AssociationVersion: resource.AssociationVersion, Scope: scope,
	})
	return sha256.Sum256(canonical)
}

func sortedResourceScopeStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
