package pidistribution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"runtime"
	"sort"
	"strings"
)

const maxManifestBytes = 16 << 20

type ManifestPlatform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion string           `json:"schema_version"`
	Platform      ManifestPlatform `json:"platform"`
	PackageName   string           `json:"package_name"`
	Version       string           `json:"version"`
	Executable    string           `json:"executable"`
	Files         []ManifestFile   `json:"files"`
	ClosureSHA256 string           `json:"closure_sha256"`
}

// PrivateAsset is caller-supplied activation authority. A zero pin is always
// unavailable, even when manifest bytes and source files exist.
type PrivateAsset struct {
	ManifestBytes          []byte
	ExpectedManifestSHA256 [32]byte
	SourceRoot             string
}

type authenticatedManifest struct {
	document Manifest
	digest   [32]byte
	closure  [32]byte
}

func authenticateManifest(asset *PrivateAsset, goos, goarch string) (authenticatedManifest, error) {
	if asset == nil || asset.ExpectedManifestSHA256 == ([32]byte{}) || len(asset.ManifestBytes) == 0 {
		return authenticatedManifest{}, ErrPrivateAssetUnavailable
	}
	if !canonicalAbsolute(asset.SourceRoot) {
		return authenticatedManifest{}, fmt.Errorf("%w: source root must be canonical and absolute", ErrInvalidManifest)
	}
	if len(asset.ManifestBytes) > maxManifestBytes {
		return authenticatedManifest{}, fmt.Errorf("%w: document too large", ErrInvalidManifest)
	}
	digest := sha256.Sum256(asset.ManifestBytes)
	if digest != asset.ExpectedManifestSHA256 {
		return authenticatedManifest{}, fmt.Errorf("%w: manifest SHA-256 mismatch", ErrInvalidManifest)
	}
	if err := rejectDuplicateKeys(asset.ManifestBytes); err != nil {
		return authenticatedManifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(asset.ManifestBytes))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return authenticatedManifest{}, fmt.Errorf("%w: decode: %v", ErrInvalidManifest, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return authenticatedManifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	closure, err := validateManifest(manifest, goos, goarch)
	if err != nil {
		return authenticatedManifest{}, err
	}
	return authenticatedManifest{document: manifest, digest: digest, closure: closure}, nil
}

func validateManifest(manifest Manifest, goos, goarch string) ([32]byte, error) {
	if manifest.SchemaVersion != ManifestSchema || manifest.PackageName != ExpectedPackageName || manifest.Version != SupportedVersion {
		return [32]byte{}, fmt.Errorf("%w: schema or package identity mismatch", ErrInvalidManifest)
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	if (goos != "darwin" && goos != "linux") || manifest.Platform.OS != goos || manifest.Platform.Architecture != goarch {
		return [32]byte{}, fmt.Errorf("%w: platform mismatch", ErrInvalidManifest)
	}
	if !validRelativePath(manifest.Executable) || reservedPackagePath(manifest.Executable) || len(manifest.Files) == 0 {
		return [32]byte{}, fmt.Errorf("%w: executable or file inventory is invalid", ErrInvalidManifest)
	}
	if !sort.SliceIsSorted(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path }) {
		return [32]byte{}, fmt.Errorf("%w: files must be sorted by normalized path", ErrInvalidManifest)
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	packageJSON := false
	executable := false
	for _, file := range manifest.Files {
		if !validRelativePath(file.Path) || reservedPackagePath(file.Path) || file.Size < 0 || !validDigest(file.SHA256) || !safeRegularMode(file.Mode) {
			return [32]byte{}, fmt.Errorf("%w: invalid file %q", ErrInvalidManifest, file.Path)
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return [32]byte{}, fmt.Errorf("%w: duplicate file %q", ErrInvalidManifest, file.Path)
		}
		seen[file.Path] = struct{}{}
		if file.Path == "package.json" {
			packageJSON = true
		}
		if file.Path == manifest.Executable {
			executable = file.Mode&0o100 != 0
		}
	}
	for filePath := range seen {
		for parent := path.Dir(filePath); parent != "."; parent = path.Dir(parent) {
			if _, fileIsAncestor := seen[parent]; fileIsAncestor {
				return [32]byte{}, fmt.Errorf("%w: regular file %q is an ancestor", ErrInvalidManifest, parent)
			}
		}
	}
	if !packageJSON || !executable {
		return [32]byte{}, fmt.Errorf("%w: package.json or executable missing", ErrInvalidManifest)
	}
	closure := ClosureDigest(manifest.Files)
	if digestText(closure) != manifest.ClosureSHA256 {
		return [32]byte{}, fmt.Errorf("%w: closure digest mismatch", ErrInvalidManifest)
	}
	return closure, nil
}

// ClosureDigest returns the canonical, length-delimited digest of an ordered
// manifest inventory. Validation requires lexicographic normalized paths.
func ClosureDigest(files []ManifestFile) [32]byte {
	hash := sha256.New()
	writeFrame(hash, []byte(closureSchema))
	for _, file := range files {
		writeFrame(hash, []byte(file.Path))
		writeFrame(hash, []byte(fmt.Sprintf("%04o", file.Mode)))
		writeFrame(hash, []byte(fmt.Sprintf("%d", file.Size)))
		digest, _ := hex.DecodeString(file.SHA256)
		writeFrame(hash, digest)
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func validRelativePath(value string) bool {
	return value != "" && value != "." && !strings.Contains(value, "\\") && !strings.ContainsRune(value, '\x00') &&
		!strings.HasPrefix(value, "/") && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../")
}

func reservedPackagePath(value string) bool {
	return value == ".complete" || strings.HasPrefix(value, ".complete/")
}

func safeRegularMode(mode uint32) bool {
	return mode != 0 && mode <= 0o777 && mode&0o022 == 0 && mode&0o700 != 0
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

// encoding/json accepts duplicate object keys. Walk the token stream first so
// security decisions never inherit its last-key-wins behavior.
func rejectDuplicateKeys(document []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		keys := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := keys[key]; exists {
				return fmt.Errorf("duplicate object key %q", key)
			}
			keys[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim(map[json.Delim]json.Delim{'{': '}', '[': ']'}[delimiter]) {
		return errors.New("invalid JSON composite")
	}
	return nil
}
