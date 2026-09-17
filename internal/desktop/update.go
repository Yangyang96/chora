package desktop

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

type UpdateInspection struct {
	Candidate        string `json:"candidate"`
	Current          string `json:"current"`
	TeamIdentifier   string `json:"teamIdentifier"`
	Notarized        bool   `json:"notarized"`
	CandidateVersion string `json:"candidateVersion"`
	CurrentVersion   string `json:"currentVersion"`
}

func VerifyUpdate(candidate, current string) (UpdateInspection, error) {
	c, e := exactAbsolute(candidate)
	if e != nil {
		return UpdateInspection{}, e
	}
	installed, e := exactAbsolute(current)
	if e != nil {
		return UpdateInspection{}, e
	}
	if runtime.GOOS != "darwin" {
		return UpdateInspection{}, errors.New("desktop updates are supported only on macOS")
	}
	for _, app := range []string{c, installed} {
		if filepath.Ext(app) != ".app" {
			return UpdateInspection{}, errors.New("update paths must name app bundles")
		}
		if _, e = run("/usr/bin/codesign", "--verify", "--deep", "--strict", "--verbose=2", app); e != nil {
			return UpdateInspection{}, fmt.Errorf("codesign verification failed: %w", e)
		}
	}
	candidateTeam, e := teamID(c)
	if e != nil {
		return UpdateInspection{}, e
	}
	currentTeam, e := teamID(installed)
	if e != nil {
		return UpdateInspection{}, e
	}
	if candidateTeam == "" || candidateTeam != currentTeam {
		return UpdateInspection{}, errors.New("candidate TeamIdentifier does not match installed app")
	}
	if candidateTeam == "not set" {
		return UpdateInspection{}, errors.New("Developer ID TeamIdentifier is not set")
	}
	for _, app := range []string{c, installed} {
		bundleID, e := plistValue(app, "CFBundleIdentifier")
		if e != nil || bundleID != "org.chora.desktop" {
			return UpdateInspection{}, errors.New("app bundle identifier is not org.chora.desktop")
		}
		executable, e := plistValue(app, "CFBundleExecutable")
		if e != nil || executable != "Chora" {
			return UpdateInspection{}, errors.New("app executable identity is not Chora")
		}
	}
	if _, e = run("/usr/bin/xcrun", "stapler", "validate", c); e != nil {
		return UpdateInspection{}, fmt.Errorf("notarization ticket validation failed: %w", e)
	}
	if _, e = run("/usr/sbin/spctl", "--assess", "--type", "execute", "--verbose=2", c); e != nil {
		return UpdateInspection{}, fmt.Errorf("Gatekeeper assessment failed: %w", e)
	}
	candidateVersion, e := bundleVersion(c)
	if e != nil {
		return UpdateInspection{}, e
	}
	currentVersion, e := bundleVersion(installed)
	if e != nil {
		return UpdateInspection{}, e
	}
	newer, e := releaseNewer(candidateVersion, currentVersion)
	if e != nil {
		return UpdateInspection{}, e
	}
	if !newer {
		return UpdateInspection{}, errors.New("candidate version must be newer than installed version")
	}
	return UpdateInspection{Candidate: c, Current: installed, TeamIdentifier: candidateTeam, Notarized: true, CandidateVersion: candidateVersion, CurrentVersion: currentVersion}, nil
}

func bundleVersion(app string) (string, error) {
	out, err := run("/usr/libexec/PlistBuddy", "-c", "Print :ChoraReleaseVersion", filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return "", fmt.Errorf("read bundle version: %w", err)
	}
	version := strings.TrimSpace(out)
	if version == "" {
		return "", errors.New("app has no bundle version")
	}
	return version, nil
}
func plistValue(app, key string) (string, error) {
	out, e := run("/usr/libexec/PlistBuddy", "-c", "Print :"+key, filepath.Join(app, "Contents", "Info.plist"))
	if e != nil {
		return "", e
	}
	return strings.TrimSpace(out), nil
}

var releasePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z.-]+)?$`)

type releaseVersion struct {
	core [3]uint64
	pre  []string
}

func parseRelease(value string) (releaseVersion, error) {
	match := releasePattern.FindStringSubmatch(value)
	if match == nil {
		return releaseVersion{}, errors.New("invalid Chora release version")
	}
	var result releaseVersion
	for i := 0; i < 3; i++ {
		result.core[i], _ = strconv.ParseUint(match[i+1], 10, 64)
	}
	if match[4] != "" {
		result.pre = strings.Split(match[4], ".")
	}
	return result, nil
}
func releaseNewer(candidate, current string) (bool, error) {
	a, e := parseRelease(candidate)
	if e != nil {
		return false, e
	}
	b, e := parseRelease(current)
	if e != nil {
		return false, e
	}
	for i := 0; i < 3; i++ {
		if a.core[i] != b.core[i] {
			return a.core[i] > b.core[i], nil
		}
	}
	if len(a.pre) == 0 || len(b.pre) == 0 {
		return len(a.pre) == 0 && len(b.pre) > 0, nil
	}
	n := len(a.pre)
	if len(b.pre) < n {
		n = len(b.pre)
	}
	for i := 0; i < n; i++ {
		if a.pre[i] == b.pre[i] {
			continue
		}
		av, ae := strconv.ParseUint(a.pre[i], 10, 64)
		bv, be := strconv.ParseUint(b.pre[i], 10, 64)
		if ae == nil && be == nil {
			return av > bv, nil
		}
		if ae == nil {
			return false, nil
		}
		if be == nil {
			return true, nil
		}
		return a.pre[i] > b.pre[i], nil
	}
	return len(a.pre) > len(b.pre), nil
}
func teamID(app string) (string, error) {
	out, e := run("/usr/bin/codesign", "-d", "--verbose=4", app)
	if e != nil {
		return "", e
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "TeamIdentifier=") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "TeamIdentifier=")), nil
		}
	}
	return "", errors.New("signed app has no TeamIdentifier")
}
func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	e := cmd.Run()
	return output.String(), e
}
