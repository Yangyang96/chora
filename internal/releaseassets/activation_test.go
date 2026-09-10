package releaseassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadActivatedReleaseMapsEveryRoleExactlyAndIsValueSafe(t *testing.T) {
	input, manifest := activationFixture(t)
	activated, err := LoadActivatedRelease(input)
	if err != nil {
		t.Fatal(err)
	}

	artifacts := make(map[string]ManifestArtifact, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	for _, binding := range manifest.Roles {
		image, err := activated.ImageForRole(binding.Role)
		if err != nil {
			t.Fatalf("role %s: %v", binding.Role, err)
		}
		artifact := artifacts[binding.ArtifactID]
		entrypoint, err := json.Marshal(artifact.Entrypoint)
		if err != nil {
			t.Fatal(err)
		}
		if image.Role != binding.Role || image.ArtifactID != binding.ArtifactID ||
			image.LocalDockerConfigImageID != artifact.Image.LocalDockerConfigImageID ||
			image.ArchiveFormat != artifact.Image.Archive.Format ||
			image.ArchivePath != filepath.Join(input.Root, filepath.FromSlash(artifact.Image.Archive.Path)) ||
			image.ArchiveSize != artifact.Image.Archive.Size || image.ArchiveSHA256 != artifact.Image.Archive.SHA256 ||
			image.PolicyID != binding.Policy.ID || image.PolicyPath != binding.Policy.Path ||
			image.PolicySHA256 != binding.Policy.SHA256 || image.ReleaseID != manifest.ReleaseID ||
			image.SpecSHA256 != manifest.SpecSHA256 || image.Platform != manifest.Platform ||
			image.EntrypointJSON != string(entrypoint) {
			t.Fatalf("role %s projection mismatch: %#v", binding.Role, image)
		}
		if artifact.Runtime == nil {
			if image.RuntimePiName != "" || image.RuntimeNodeVersion != "" {
				t.Fatalf("role %s fabricated Runtime identity: %#v", binding.Role, image)
			}
		} else if image.RuntimePiName != artifact.Runtime.Pi.Name || image.RuntimePiVersion != artifact.Runtime.Pi.Version ||
			image.RuntimePiNPMIntegrity != artifact.Runtime.Pi.NPMIntegrity || image.RuntimeNodeVersion != artifact.Runtime.Node.Version ||
			image.RuntimeNodeExecutable != artifact.Runtime.Node.Executable || image.RuntimeNodeVersionOutput != artifact.Runtime.Node.VersionOutput {
			t.Fatalf("role %s Runtime projection mismatch: %#v", binding.Role, image)
		}
	}

	verifier, err := activated.ImageForRole(RoleIndependentVerifier)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := activated.ImageForRole(RoleManagedPiRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if verifier.ArtifactID != managed.ArtifactID || verifier.PolicyID == managed.PolicyID {
		t.Fatal("explicit role alias did not retain its own role policy and exact shared artifact")
	}

	managed.ArtifactID = "mutated"
	managed.Platform.OS = "mutated"
	managed.RuntimePiName = "mutated"
	managed.EntrypointJSON = `["mutated"]`
	again, err := activated.ImageForRole(RoleManagedPiRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if again.ArtifactID == "mutated" || again.Platform.OS == "mutated" || again.RuntimePiName == "mutated" || again.EntrypointJSON == `["mutated"]` {
		t.Fatal("returned image mutated activated release")
	}
	if _, err := activated.ImageForRole("runtime"); err == nil {
		t.Fatal("unknown role was accepted")
	}
	if _, err := (ActivatedRelease{}).ImageForRole(RoleManagedPiRuntime); err == nil {
		t.Fatal("zero release returned an image")
	}
}

func TestActivatedReleaseReadsOnlyExactRolePolicyFromExplicitReleaseRoot(t *testing.T) {
	input, _ := activationFixture(t)
	release, err := LoadActivatedRelease(input)
	if err != nil {
		t.Fatal(err)
	}
	image, err := release.ImageForRole(RoleIndependentVerifier)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(input.Root, filepath.FromSlash(image.PolicyPath)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := release.ReadPolicyForRole(input.Root, RoleIndependentVerifier)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("role policy = %q, err=%v", got, err)
	}
	got[0] ^= 0xff
	again, err := release.ReadPolicyForRole(input.Root, RoleIndependentVerifier)
	if err != nil || !bytes.Equal(again, want) {
		t.Fatal("returned role policy bytes aliased release authority")
	}
	if _, err := release.ReadPolicyForRole(input.Root, "unknown_role"); err == nil {
		t.Fatal("unknown role read arbitrary policy path")
	}
}

func TestActivatedReleaseRolePolicyRejectsEmptyWrongSymlinkHardlinkAndDriftedAuthority(t *testing.T) {
	t.Run("empty root", func(t *testing.T) {
		input, _ := activationFixture(t)
		release, err := LoadActivatedRelease(input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := release.ReadPolicyForRole("", RoleIndependentVerifier); err == nil {
			t.Fatal("empty activated release root was accepted")
		}
	})

	t.Run("wrong root", func(t *testing.T) {
		input, _ := activationFixture(t)
		release, err := LoadActivatedRelease(input)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := release.ReadPolicyForRole(canonicalTempDir(t), RoleIndependentVerifier); err == nil {
			t.Fatal("unrelated activated release root was accepted")
		}
	})

	t.Run("root symlink", func(t *testing.T) {
		input, _ := activationFixture(t)
		release, err := LoadActivatedRelease(input)
		if err != nil {
			t.Fatal(err)
		}
		linked := filepath.Join(canonicalTempDir(t), "release-link")
		if err := os.Symlink(input.Root, linked); err != nil {
			t.Fatal(err)
		}
		if _, err := release.ReadPolicyForRole(linked, RoleIndependentVerifier); err == nil {
			t.Fatal("symlink activated release root was accepted")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, []byte)
	}{
		{name: "policy symlink", mutate: func(t *testing.T, path string, exact []byte) {
			target := filepath.Join(canonicalTempDir(t), "outside-policy.json")
			if err := os.WriteFile(target, exact, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "policy hardlink", mutate: func(t *testing.T, path string, exact []byte) {
			target := filepath.Join(canonicalTempDir(t), "outside-policy.json")
			if err := os.WriteFile(target, exact, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "policy digest drift", mutate: func(t *testing.T, path string, exact []byte) {
			if err := os.WriteFile(path, append(exact, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, _ := activationFixture(t)
			release, err := LoadActivatedRelease(input)
			if err != nil {
				t.Fatal(err)
			}
			image, err := release.ImageForRole(RoleIndependentVerifier)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(input.Root, filepath.FromSlash(image.PolicyPath))
			exact, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, path, exact)
			if _, err := release.ReadPolicyForRole(input.Root, RoleIndependentVerifier); err == nil {
				t.Fatal("unsafe or drifted activated role policy was accepted")
			}
		})
	}
}

func TestLoadActivatedReleaseRejectsManifestPinDrift(t *testing.T) {
	input, _ := activationFixture(t)
	path := filepath.Join(input.Root, filepath.FromSlash(input.ManifestPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadActivatedRelease(input); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("manifest drift error = %v", err)
	}
}

func TestLoadActivatedReleaseRejectsMissingAndOversizedManifest(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		input, _ := activationFixture(t)
		if err := os.Remove(filepath.Join(input.Root, filepath.FromSlash(input.ManifestPath))); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("missing manifest was accepted")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		input, _ := activationFixture(t)
		raw := bytes.Repeat([]byte{'x'}, maxDocumentBytes+1)
		if err := os.WriteFile(filepath.Join(input.Root, filepath.FromSlash(input.ManifestPath)), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		input.ManifestSHA256 = activationDigest(raw)
		if _, err := LoadActivatedRelease(input); err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("oversized manifest error = %v", err)
		}
	})
}

func TestLoadActivatedReleaseRejectsInstalledFileAndEvidenceDrift(t *testing.T) {
	t.Run("artifact file", func(t *testing.T) {
		input, manifest := activationFixture(t)
		path := filepath.Join(input.Root, filepath.FromSlash(manifest.Artifacts[0].Recipe.Path))
		if err := os.WriteFile(path, []byte("tampered\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("artifact file drift was accepted")
		}
	})

	t.Run("Docker archive", func(t *testing.T) {
		input, manifest := activationFixture(t)
		path := filepath.Join(input.Root, filepath.FromSlash(manifest.Artifacts[0].Image.Archive.Path))
		if err := os.WriteFile(path, []byte("not an archive\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("Docker archive drift was accepted")
		}
	})
}

func TestLoadActivatedReleaseRejectsUnsafeRootsAndManifestPaths(t *testing.T) {
	t.Run("relative root", func(t *testing.T) {
		input, _ := activationFixture(t)
		input.Root = "relative/root"
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("relative root was accepted")
		}
	})

	t.Run("noncanonical root", func(t *testing.T) {
		input, _ := activationFixture(t)
		input.Root += string(os.PathSeparator)
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("noncanonical root was accepted")
		}
	})

	t.Run("root symlink", func(t *testing.T) {
		input, _ := activationFixture(t)
		parent := canonicalTempDir(t)
		linkedRoot := filepath.Join(parent, "release-root-link")
		if err := os.Symlink(input.Root, linkedRoot); err != nil {
			t.Fatal(err)
		}
		input.Root = linkedRoot
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("symlink root was accepted")
		}
	})

	t.Run("filesystem root", func(t *testing.T) {
		input, _ := activationFixture(t)
		input.Root = filepath.VolumeName(input.Root) + string(os.PathSeparator)
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("filesystem root was accepted")
		}
	})

	for _, path := range []string{"../release.json", "/release.json", "installed/../release.json", "./installed/release.json"} {
		t.Run(path, func(t *testing.T) {
			input, _ := activationFixture(t)
			input.ManifestPath = path
			if _, err := LoadActivatedRelease(input); err == nil {
				t.Fatalf("unsafe manifest path %q was accepted", path)
			}
		})
	}

	t.Run("manifest symlink", func(t *testing.T) {
		input, _ := activationFixture(t)
		linkPath := "installed/release-link.json"
		if err := os.Symlink(filepath.Base(input.ManifestPath), filepath.Join(input.Root, filepath.FromSlash(linkPath))); err != nil {
			t.Fatal(err)
		}
		input.ManifestPath = linkPath
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("symlink manifest was accepted")
		}
	})
}

func TestLoadActivatedReleaseRejectsReleasePlatformAndAmbiguity(t *testing.T) {
	t.Run("empty release", func(t *testing.T) {
		input, _ := activationFixture(t)
		input.ReleaseID = ""
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("empty release ID was accepted")
		}
	})

	t.Run("wrong release", func(t *testing.T) {
		input, _ := activationFixture(t)
		input.ReleaseID = "another-release"
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("wrong release ID was accepted")
		}
	})

	t.Run("wrong platform", func(t *testing.T) {
		input, manifest := activationFixture(t)
		manifest.Platform.Architecture = "amd64"
		replaceActivationManifest(t, &input, manifest)
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("non-official platform was accepted")
		}
	})

	t.Run("ambiguous role", func(t *testing.T) {
		input, manifest := activationFixture(t)
		manifest.Roles[1] = manifest.Roles[0]
		replaceActivationManifest(t, &input, manifest)
		if _, err := LoadActivatedRelease(input); err == nil {
			t.Fatal("ambiguous role binding was accepted")
		}
	})
}

func TestLoadActivatedReleaseRequiresExactLowercaseManifestPin(t *testing.T) {
	input, _ := activationFixture(t)
	input.ManifestSHA256 = strings.ToUpper(input.ManifestSHA256)
	if _, err := LoadActivatedRelease(input); err == nil {
		t.Fatal("uppercase manifest pin was accepted")
	}
	input.ManifestSHA256 = strings.Repeat("0", 63)
	if _, err := LoadActivatedRelease(input); err == nil {
		t.Fatal("short manifest pin was accepted")
	}
}

func activationFixture(t *testing.T) (ActivationInput, Manifest) {
	t.Helper()
	root, spec, resolution := fixture(t)
	manifest, err := Generate(root, spec, resolution)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := "installed/release-manifest.json"
	write(t, root, manifestPath, string(raw))
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return ActivationInput{
		Root:           canonicalRoot,
		ManifestPath:   manifestPath,
		ManifestSHA256: activationDigest(raw),
		ReleaseID:      manifest.ReleaseID,
	}, manifest
}

func replaceActivationManifest(t *testing.T, input *ActivationInput, manifest Manifest) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(input.Root, filepath.FromSlash(input.ManifestPath))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	input.ManifestSHA256 = activationDigest(raw)
}

func activationDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
