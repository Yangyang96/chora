package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
)

// PathPiSource is a PATH-discovered, user-installed Pi for the M2-S1 Local
// Connected choice. Unlike LocalPiSource it has no Chora-managed package root
// or closure: Chora captures the resolved executable, its version, and its
// SHA-256 at discovery and re-verifies them at launch for drift. Discovery and
// installation are outside this value contract.
type PathPiSourceParams struct {
	ExecutablePath   string
	Version          string
	ExecutableSHA256 [32]byte
}

type PathPiSourceRecord struct {
	ExecutablePath   string
	Version          string
	ExecutableSHA256 [32]byte
	SourceIdentity   [32]byte
}

type PathPiSource struct {
	executablePath   string
	version          string
	executableSHA256 [32]byte
	sourceIdentity   [32]byte
}

func NewPathPiSource(params PathPiSourceParams) (PathPiSource, error) {
	if !validAbsoluteCleanPath(params.ExecutablePath) || !versionAtLeast(params.Version, RuntimeVersion) ||
		params.ExecutableSHA256 == ([32]byte{}) {
		return PathPiSource{}, errors.New("invalid PATH Pi source identity")
	}
	source := PathPiSource{
		executablePath: params.ExecutablePath, version: params.Version,
		executableSHA256: params.ExecutableSHA256,
	}
	source.sourceIdentity = pathSourceIdentity(source)
	return source, nil
}

func RestorePathPiSource(record PathPiSourceRecord) (PathPiSource, error) {
	source, err := NewPathPiSource(PathPiSourceParams{
		ExecutablePath: record.ExecutablePath, Version: record.Version, ExecutableSHA256: record.ExecutableSHA256,
	})
	if err != nil || record.SourceIdentity == ([32]byte{}) || source.sourceIdentity != record.SourceIdentity {
		return PathPiSource{}, errors.New("PATH Pi source identity drift")
	}
	return source, nil
}

func (source PathPiSource) Record() PathPiSourceRecord {
	return PathPiSourceRecord{
		ExecutablePath: source.executablePath, Version: source.version,
		ExecutableSHA256: source.executableSHA256, SourceIdentity: source.sourceIdentity,
	}
}

func (source PathPiSource) Configured() bool { return source != (PathPiSource{}) }

func (source PathPiSource) valid() bool {
	restored, err := RestorePathPiSource(source.Record())
	return err == nil && restored == source
}

func (source PathPiSource) ExecutablePath() string     { return source.executablePath }
func (source PathPiSource) Version() string            { return source.version }
func (source PathPiSource) ExecutableSHA256() [32]byte { return source.executableSHA256 }
func (source PathPiSource) SourceIdentity() [32]byte   { return source.sourceIdentity }

func pathSourceIdentity(source PathPiSource) [32]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		sourceContractVersion, "path_pi", source.version, source.executablePath,
		hex.EncodeToString(source.executableSHA256[:]),
	}, "\x00")))
}

func validAbsoluteCleanPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

// versionAtLeast compares dotted numeric versions (assumed x.y.z) and reports
// whether value is at least floor.
func versionAtLeast(value, floor string) bool {
	left := splitVersion(value)
	right := splitVersion(floor)
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] > right[i]
		}
	}
	return len(left) >= len(right)
}

func splitVersion(value string) []int {
	fields := strings.Split(value, ".")
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil {
			return nil
		}
		parts = append(parts, number)
	}
	return parts
}
