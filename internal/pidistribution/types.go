package pidistribution

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	SupportedVersion    = "0.84.2"
	ExpectedPackageName = "@earendil-works/pi-coding-agent"
	ExecutableName      = "pi"
	ManifestSchema      = "chora.pi-private-asset.v1"
	selectionSchema     = "chora.pi-distribution-selection.v1"
	closureSchema       = "chora.pi-package-closure.v1"
	completeSchema      = "chora.pi-private-install-complete.v1"
)

var (
	ErrNoUsablePi              = errors.New("no usable Pi distribution")
	ErrPrivateAssetUnavailable = errors.New("authenticated private Pi asset unavailable")
	ErrInvalidManifest         = errors.New("invalid private Pi asset manifest")
	ErrUnsafeClosure           = errors.New("unsafe Pi package closure")
	ErrVersionRejected         = errors.New("Pi version probe rejected")
	ErrSelectionDrift          = errors.New("Pi selection identity drift")
	ErrRemovalInterrupted      = errors.New("private Pi removal interrupted")
)

type SelectionKind string

const (
	SelectionPATH    SelectionKind = "path"
	SelectionPrivate SelectionKind = "private"
)

// SelectionRecord is the complete persisted form of a Selection. Identity
// detects omission or modification of any field.
type SelectionRecord struct {
	Schema           string
	Kind             SelectionKind
	Path             string
	ResolvedPath     string
	PackageRoot      string
	Version          string
	ExecutableSHA256 [32]byte
	ClosureSHA256    [32]byte
	Identity         [32]byte
}

// Selection is immutable: construction and restoration copy every value and
// expose only value-typed fields.
type Selection struct {
	record SelectionRecord
}

func newSelection(record SelectionRecord) (Selection, error) {
	record.Schema = selectionSchema
	if record.Kind != SelectionPATH && record.Kind != SelectionPrivate {
		return Selection{}, errors.New("invalid Pi selection kind")
	}
	if record.Version != SupportedVersion || record.ExecutableSHA256 == ([32]byte{}) || record.ClosureSHA256 == ([32]byte{}) {
		return Selection{}, errors.New("invalid Pi selection values")
	}
	for _, path := range []string{record.Path, record.ResolvedPath, record.PackageRoot} {
		if !canonicalAbsolute(path) {
			return Selection{}, errors.New("Pi selection paths must be canonical absolute paths")
		}
	}
	if !pathWithin(record.PackageRoot, record.ResolvedPath) {
		return Selection{}, errors.New("Pi executable is outside its package root")
	}
	record.Identity = selectionIdentity(record)
	return Selection{record: record}, nil
}

func RestoreSelection(record SelectionRecord) (Selection, error) {
	want := record.Identity
	selection, err := newSelection(record)
	if err != nil || want == ([32]byte{}) || selection.record.Identity != want || record.Schema != selectionSchema {
		return Selection{}, ErrSelectionDrift
	}
	return selection, nil
}

func (selection Selection) Record() SelectionRecord    { return selection.record }
func (selection Selection) Configured() bool           { return selection.record.Identity != ([32]byte{}) }
func (selection Selection) Kind() SelectionKind        { return selection.record.Kind }
func (selection Selection) Path() string               { return selection.record.Path }
func (selection Selection) ResolvedPath() string       { return selection.record.ResolvedPath }
func (selection Selection) PackageRoot() string        { return selection.record.PackageRoot }
func (selection Selection) Version() string            { return selection.record.Version }
func (selection Selection) ExecutableSHA256() [32]byte { return selection.record.ExecutableSHA256 }
func (selection Selection) ClosureSHA256() [32]byte    { return selection.record.ClosureSHA256 }
func (selection Selection) Identity() [32]byte         { return selection.record.Identity }

// LocalPiSourceData is a lossless integration payload for agent/pi. The current
// agent/pi LocalPiSource contract cannot consume it without dropping the
// resolved path and closure binding, so this package intentionally provides no
// lossy conversion helper.
type LocalPiSourceData struct {
	ExecutablePath    string
	ResolvedPath      string
	PackageRoot       string
	RuntimeVersion    string
	ExecutableSHA256  [32]byte
	ClosureSHA256     [32]byte
	SelectionIdentity [32]byte
}

func (selection Selection) LocalPiSourceData() LocalPiSourceData {
	return LocalPiSourceData{
		ExecutablePath: selection.Path(), ResolvedPath: selection.ResolvedPath(),
		PackageRoot: selection.PackageRoot(), RuntimeVersion: selection.Version(),
		ExecutableSHA256: selection.ExecutableSHA256(), ClosureSHA256: selection.ClosureSHA256(),
		SelectionIdentity: selection.Identity(),
	}
}

func selectionIdentity(record SelectionRecord) [32]byte {
	hash := sha256.New()
	writeFrame(hash, []byte(selectionSchema))
	writeFrame(hash, []byte(record.Kind))
	writeFrame(hash, []byte(record.Path))
	writeFrame(hash, []byte(record.ResolvedPath))
	writeFrame(hash, []byte(record.PackageRoot))
	writeFrame(hash, []byte(record.Version))
	writeFrame(hash, record.ExecutableSHA256[:])
	writeFrame(hash, record.ClosureSHA256[:])
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

type frameWriter interface{ Write([]byte) (int, error) }

func writeFrame(writer frameWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func canonicalAbsolute(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) &&
		relative != "" && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func digestText(digest [32]byte) string { return fmt.Sprintf("%x", digest[:]) }
