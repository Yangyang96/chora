package piinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type standardDownloader struct{}

func (standardDownloader) Download(ctx context.Context, request DownloadRequest) error {
	u, err := url.Parse(request.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() != "registry.npmjs.org" || u.User != nil {
		return errors.New("download URL is outside the fixed registry origin")
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 || req.URL.Scheme != "https" || req.URL.Hostname() != "registry.npmjs.org" || req.URL.User != nil {
				return errors.New("download redirect is outside the fixed registry origin")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > request.MaximumBytes {
		return errors.New("download exceeds maximum size")
	}
	file, err := os.OpenFile(request.DestinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			_ = os.Remove(request.DestinationPath)
		}
	}()
	written, err := io.Copy(file, io.LimitReader(resp.Body, request.MaximumBytes+1))
	if err != nil {
		return err
	}
	if written > request.MaximumBytes {
		return errors.New("download exceeds maximum size")
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

type standardToolchainProbe struct{ runner CommandRunner }

func (p standardToolchainProbe) Probe(ctx context.Context) (Toolchain, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return Toolchain{}, fmt.Errorf("find Node.js: %w", err)
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return Toolchain{}, fmt.Errorf("find npm: %w", err)
	}
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		return Toolchain{}, fmt.Errorf("resolve Node.js: %w", err)
	}
	npm, err = filepath.EvalSymlinks(npm)
	if err != nil {
		return Toolchain{}, fmt.Errorf("resolve npm: %w", err)
	}
	if err := validateDiscoveredExecutable(node); err != nil {
		return Toolchain{}, fmt.Errorf("validate Node.js: %w", err)
	}
	if err := validateDiscoveredExecutable(npm); err != nil {
		return Toolchain{}, fmt.Errorf("validate npm: %w", err)
	}
	nodeResult, err := p.runner.Run(ctx, Command{Executable: node, Arguments: []string{"--version"}, OutputLimit: 4096})
	if err != nil || nodeResult.ExitCode != 0 {
		return Toolchain{}, errors.New("Node.js version probe failed")
	}
	npmResult, err := p.runner.Run(ctx, Command{Executable: npm, Arguments: []string{"--version"}, OutputLimit: 4096})
	if err != nil || npmResult.ExitCode != 0 {
		return Toolchain{}, errors.New("npm version probe failed")
	}
	return Toolchain{NodeExecutable: node, NodeVersion: strings.TrimSpace(nodeResult.Stdout), NPMExecutable: npm, NPMVersion: strings.TrimSpace(npmResult.Stdout)}, nil
}

func validateDiscoveredExecutable(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("path is not absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("path is not an executable regular file")
	}
	return nil
}

type standardCommandRunner struct{}

func (standardCommandRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	if command.OutputLimit <= 0 {
		command.OutputLimit = 32 << 10
	}
	cmd := exec.CommandContext(ctx, command.Executable, command.Arguments...)
	cmd.Dir = command.Directory
	if command.Environment != nil {
		cmd.Env = append([]string(nil), command.Environment...)
	}
	stdout := &limitedBuffer{limit: command.OutputLimit}
	stderr := &limitedBuffer{limit: command.OutputLimit}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	result := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		_, _ = b.buffer.Write(p)
	} else {
		b.truncated = true
	}
	return n, nil
}

func (b *limitedBuffer) String() string {
	value := b.buffer.String()
	if b.truncated {
		value += "\n[output truncated]"
	}
	return value
}

func validateToolchain(toolchain Toolchain, c Contract) error {
	if !filepath.IsAbs(toolchain.NodeExecutable) || !filepath.IsAbs(toolchain.NPMExecutable) {
		return errors.New("toolchain executable paths must be absolute")
	}
	if lessSemver(strings.TrimPrefix(toolchain.NodeVersion, "v"), c.MinimumNodeVersion) {
		return fmt.Errorf("Node.js %s is below required %s", toolchain.NodeVersion, c.MinimumNodeVersion)
	}
	parts := strings.Split(strings.TrimPrefix(toolchain.NPMVersion, "v"), ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil || major < c.MinimumNPMMajor {
		return fmt.Errorf("npm %s is below required major %d", toolchain.NPMVersion, c.MinimumNPMMajor)
	}
	return nil
}

func lessSemver(left, right string) bool {
	parse := func(value string) [3]int {
		var out [3]int
		parts := strings.Split(value, ".")
		for i := 0; i < len(parts) && i < 3; i++ {
			out[i], _ = strconv.Atoi(parts[i])
		}
		return out
	}
	l, r := parse(left), parse(right)
	for i := range l {
		if l[i] != r[i] {
			return l[i] < r[i]
		}
	}
	return false
}
