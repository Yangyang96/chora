package piinstall

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	fixedPackage        = "@earendil-works/pi-coding-agent"
	fixedVersion        = "0.85.1"
	fixedRegistry       = "https://registry.npmjs.org"
	fixedTarball        = "https://registry.npmjs.org/@earendil-works/pi-coding-agent/-/pi-coding-agent-0.85.1.tgz"
	fixedSRI            = "sha512-FGRN+OHbWaefBPGaTggAdLjrIHW+s2PzLyglz/5dfLzb9of7uuXMXYC0fJIeZTw+shS32o2cuQ9jF7YSDuL/oQ=="
	fixedManifestSHA256 = "f510371be769d1b1b70e339d5a691b064ebed73a7bcabff8ab38825234000bd9"
	fixedLockSHA256     = "9e32f53b8f44535d28b4a818a02806c628baa6efae55ff0826ac2b7e293a15d3"
	fixedExecutable     = "dist/bundle/cli.js"
)

//go:embed assets/package.json
var fixedManifest []byte

//go:embed assets/package-lock.json
var fixedLock []byte

var npmArgumentTemplate = []string{
	"ci", "--prefix", "{consumer}", "--ignore-scripts", "--omit=dev",
	"--no-audit", "--no-fund", "--registry=" + fixedRegistry,
	"--cache", "{cache}", "--userconfig", "{userconfig}", "--globalconfig", "{globalconfig}",
}

func FrozenContract() Contract {
	c := Contract{
		Package: fixedPackage, Version: fixedVersion, Registry: fixedRegistry,
		TarballURL: fixedTarball, TarballSRI: fixedSRI,
		ConsumerManifest: append([]byte(nil), fixedManifest...),
		ConsumerLock:     append([]byte(nil), fixedLock...), ConsumerLockSHA256: fixedLockSHA256,
		ExecutableRelative: fixedExecutable, MinimumNodeVersion: "22.19.0", MinimumNPMMajor: 11,
		MaxTarballBytes: 64 << 20, NPMArguments: append([]string(nil), npmArgumentTemplate...),
		LifecycleScriptsOff: true,
	}
	c.Digest = contractDigest(c)
	return c
}

func contractDigest(c Contract) string {
	payload := struct {
		Package, Version, Registry, TarballURL, TarballSRI, ManifestSHA, LockSHA, Executable, Node string
		NPMMajor                                                                                   int
		MaxBytes                                                                                   int64
		Args                                                                                       []string
		ScriptsOff                                                                                 bool
	}{c.Package, c.Version, c.Registry, c.TarballURL, c.TarballSRI,
		hexSHA256(c.ConsumerManifest), c.ConsumerLockSHA256, c.ExecutableRelative,
		c.MinimumNodeVersion, c.MinimumNPMMajor, c.MaxTarballBytes,
		append([]string(nil), c.NPMArguments...), c.LifecycleScriptsOff}
	b, _ := json.Marshal(payload)
	return hexSHA256(b)
}

func cloneContract(c Contract) Contract {
	c.ConsumerManifest = append([]byte(nil), c.ConsumerManifest...)
	c.ConsumerLock = append([]byte(nil), c.ConsumerLock...)
	c.NPMArguments = append([]string(nil), c.NPMArguments...)
	return c
}

func validateContract(c Contract, enforceFrozen bool) error {
	if hexSHA256(c.ConsumerLock) != c.ConsumerLockSHA256 {
		return fmt.Errorf("frozen consumer lock digest changed")
	}
	if !c.LifecycleScriptsOff || !contains(c.NPMArguments, "--ignore-scripts") {
		return fmt.Errorf("frozen Pi contract changed")
	}
	if enforceFrozen && (hexSHA256(c.ConsumerManifest) != fixedManifestSHA256 || c.ConsumerLockSHA256 != fixedLockSHA256 || c.Package != fixedPackage || c.Version != fixedVersion || c.Registry != fixedRegistry || c.TarballURL != fixedTarball || c.TarballSRI != fixedSRI || c.ExecutableRelative != fixedExecutable || c.Digest != FrozenContract().Digest) {
		return fmt.Errorf("embedded Pi contract differs from accepted identity")
	}
	var lock lockDocument
	if err := json.Unmarshal(c.ConsumerLock, &lock); err != nil {
		return fmt.Errorf("parse frozen consumer lock: %w", err)
	}
	if err := validateLockDocument(lock, c); err != nil {
		return err
	}
	return nil
}

func hexSHA256(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
