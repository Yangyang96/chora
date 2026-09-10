package runtimeboundary

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"net"
	"path/filepath"
)

const RendererVersion = "chora.darwin-boundary.v1"

//go:embed profile.sb.tmpl
var profileTemplate []byte

type Config struct {
	Workspace       string
	RuntimeHomeFile string
	ForbiddenRead   string
	ControlPlane    string
	LoopbackAddress string
	OutsideWrite    string
}

type ProbeReport struct {
	ForbiddenReadDenied       bool `json:"forbidden_read_denied"`
	ControlPlaneDenied        bool `json:"control_plane_denied"`
	LoopbackDenied            bool `json:"loopback_denied"`
	OutsideWriteDenied        bool `json:"outside_write_denied"`
	WorkspaceReadWriteAllowed bool `json:"workspace_read_write_allowed"`
	RuntimeHomeReadAllowed    bool `json:"runtime_home_read_allowed"`
}

func ProfileTemplateSHA256() string {
	sum := sha256.Sum256(profileTemplate)
	return hex.EncodeToString(sum[:])
}

func validateConfig(config Config) error {
	paths := []string{
		config.Workspace,
		config.RuntimeHomeFile,
		config.ForbiddenRead,
		config.ControlPlane,
		config.OutsideWrite,
	}
	for _, path := range paths {
		if path == "" || !filepath.IsAbs(path) {
			return errors.New("runtime boundary paths must be absolute")
		}
	}
	host, port, err := net.SplitHostPort(config.LoopbackAddress)
	if err != nil || host != "127.0.0.1" || port == "" {
		return errors.New("loopback address must be an explicit 127.0.0.1 TCP address")
	}
	return nil
}

func normalizeConfig(config Config) (Config, error) {
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	var err error
	for destination, source := range map[*string]string{
		&config.Workspace:       config.Workspace,
		&config.RuntimeHomeFile: config.RuntimeHomeFile,
		&config.ForbiddenRead:   config.ForbiddenRead,
		&config.ControlPlane:    config.ControlPlane,
	} {
		*destination, err = filepath.EvalSymlinks(source)
		if err != nil {
			return Config{}, err
		}
	}
	outsideParent, err := filepath.EvalSymlinks(filepath.Dir(config.OutsideWrite))
	if err != nil {
		return Config{}, err
	}
	config.OutsideWrite = filepath.Join(outsideParent, filepath.Base(config.OutsideWrite))
	return config, nil
}

func probeArgs(config Config) []string {
	return []string{
		"--workspace", config.Workspace,
		"--runtime-home-file", config.RuntimeHomeFile,
		"--forbidden-read", config.ForbiddenRead,
		"--control-plane", config.ControlPlane,
		"--loopback", config.LoopbackAddress,
		"--outside-write", config.OutsideWrite,
	}
}
