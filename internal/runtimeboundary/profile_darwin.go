//go:build darwin

package runtimeboundary

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/template"

	"github.com/Yangyang96/chora/internal/processbound"
)

func Render(config Config) ([]byte, error) {
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	data := struct {
		Workspace     string
		ForbiddenRead string
		ControlPlane  string
	}{
		Workspace:     strconv.Quote(config.Workspace),
		ForbiddenRead: strconv.Quote(config.ForbiddenRead),
		ControlPlane:  strconv.Quote(config.ControlPlane),
	}
	parsed, err := template.New("darwin-boundary").Option("missingkey=error").Parse(string(profileTemplate))
	if err != nil {
		return nil, fmt.Errorf("parse profile template: %w", err)
	}
	var rendered bytes.Buffer
	if err := parsed.Execute(&rendered, data); err != nil {
		return nil, fmt.Errorf("render profile: %w", err)
	}
	return rendered.Bytes(), nil
}

func Run(ctx context.Context, config Config, probeBinary string) (ProbeReport, error) {
	config, err := normalizeConfig(config)
	if err != nil {
		return ProbeReport{}, err
	}
	profile, err := Render(config)
	if err != nil {
		return ProbeReport{}, err
	}
	if probeBinary == "" {
		return ProbeReport{}, fmt.Errorf("probe binary is required")
	}
	if _, err := processbound.Run(ctx, processbound.Spec{
		Name:        config.ControlPlane,
		Args:        []string{"version"},
		StdoutLimit: 64 << 10,
		StderrLimit: 64 << 10,
	}); err != nil {
		return ProbeReport{}, fmt.Errorf("control-plane preflight failed: %w", err)
	}
	profileFile, err := os.CreateTemp("", "chora-runtime-boundary-*.sb")
	if err != nil {
		return ProbeReport{}, fmt.Errorf("create profile: %w", err)
	}
	profilePath := profileFile.Name()
	defer os.Remove(profilePath)
	if _, err := profileFile.Write(profile); err != nil {
		profileFile.Close()
		return ProbeReport{}, fmt.Errorf("write profile: %w", err)
	}
	if err := profileFile.Close(); err != nil {
		return ProbeReport{}, fmt.Errorf("close profile: %w", err)
	}

	result, err := processbound.Run(ctx, processbound.Spec{
		Name:        "/usr/bin/sandbox-exec",
		Args:        append([]string{"-f", profilePath, probeBinary}, probeArgs(config)...),
		StdoutLimit: 64 << 10,
		StderrLimit: 64 << 10,
	})
	if err != nil {
		return ProbeReport{}, fmt.Errorf("sandboxed probe failed: %w: %s", err, result.Stderr)
	}
	var report ProbeReport
	if err := json.Unmarshal(result.Stdout, &report); err != nil {
		return ProbeReport{}, fmt.Errorf("decode probe report: %w", err)
	}
	return report, nil
}
