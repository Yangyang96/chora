package dockersupervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	trustAnchorDigest = "3444d3f6d9ef36946ff29ef382e402017a96ff6c97f7b8ae08643acceb2e0dac"
	trustAnchorPath   = "/run/chora/trust/starpoint-root-ca-2048-g2.pem"
)

type taskProjection struct {
	auth    []byte
	trust   []byte
	secrets [][]byte
}

func loadTaskProjection(credentialSource, trustSource string) (taskProjection, error) {
	authSource, authInfo, err := readProjectionSource(credentialSource)
	if err != nil || authInfo.Mode().Perm()&0o077 != 0 {
		return taskProjection{}, errors.New("Pi OAuth source must be a regular owner-only file")
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(authSource, &document) != nil {
		return taskProjection{}, errors.New("Pi OAuth source is invalid JSON")
	}
	entry, ok := document["openai-codex"]
	entry = bytes.TrimSpace(entry)
	if !ok || len(entry) == 0 || string(entry) == "null" || entry[0] != '{' {
		return taskProjection{}, errors.New("Pi OAuth source has no openai-codex entry")
	}
	auth, err := json.Marshal(map[string]json.RawMessage{"openai-codex": entry})
	if err != nil {
		return taskProjection{}, errors.New("project Pi OAuth source")
	}
	auth = append(auth, '\n')
	secrets, err := credentialScalarValues(entry)
	if err != nil || len(secrets) == 0 {
		return taskProjection{}, errors.New("Pi OAuth source has no redaction-safe credential values")
	}

	trust, _, err := readProjectionSource(trustSource)
	if err != nil {
		return taskProjection{}, errors.New("enterprise trust source must be a regular file")
	}
	digest := sha256.Sum256(trust)
	if hex.EncodeToString(digest[:]) != trustAnchorDigest {
		return taskProjection{}, errors.New("enterprise trust source identity mismatch")
	}
	return taskProjection{auth: auth, trust: trust, secrets: secrets}, nil
}

func credentialScalarValues(entry []byte) ([][]byte, error) {
	var value any
	if err := json.Unmarshal(entry, &value); err != nil {
		return nil, err
	}
	unique := map[string]struct{}{}
	var collect func(any)
	collect = func(current any) {
		switch typed := current.(type) {
		case string:
			// OAuth tokens and account identifiers are never short labels such as
			// "oauth". Avoid treating those schema labels as credentials.
			if len(typed) >= 8 {
				unique[typed] = struct{}{}
			}
		case []any:
			for _, item := range typed {
				collect(item)
			}
		case map[string]any:
			for _, item := range typed {
				collect(item)
			}
		}
	}
	collect(value)
	secrets := make([][]byte, 0, len(unique))
	for secret := range unique {
		secrets = append(secrets, []byte(secret))
	}
	return secrets, nil
}

func (projection *taskProjection) clear() {
	clear(projection.auth)
	clear(projection.trust)
	for _, secret := range projection.secrets {
		clear(secret)
	}
	projection.auth = nil
	projection.trust = nil
	projection.secrets = nil
}

func readProjectionSource(path string) ([]byte, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("projection source is not a regular file")
	}
	data, err := io.ReadAll(file)
	return data, info, err
}

func (supervisor *Supervisor) waitForContainer(ctx context.Context, name string) error {
	waitContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		result, err := supervisor.config.Runner.Run(waitContext, Command{Args: []string{"inspect", name}})
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		if waitContext.Err() != nil {
			return errors.New("attempt container did not become inspectable")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (supervisor *Supervisor) injectTaskProjection(ctx context.Context, name string, projection taskProjection) error {
	trustCommand := "set -eu; umask 022; cat > " + trustAnchorPath + ".tmp; chmod 0444 " + trustAnchorPath + ".tmp; mv " + trustAnchorPath + ".tmp " + trustAnchorPath + "; chmod 0555 /run/chora/trust"
	if err := supervisor.runOKInput(ctx, []string{"exec", "--user", "0:0", "-i", name, "/bin/sh", "-c", trustCommand}, projection.trust); err != nil {
		return fmt.Errorf("project enterprise trust: %w", err)
	}
	trustProbe := fmt.Sprintf("const f=require('fs'),c=require('crypto'),p=%q,d=c.createHash('sha256').update(f.readFileSync(p)).digest('hex'),s=f.statSync(p),r=f.statSync('/run/chora/trust');if(d!==%q||(s.mode&511)!==292||s.uid!==0||s.gid!==0||(r.mode&511)!==365||r.uid!==0||r.gid!==0)process.exit(2)", trustAnchorPath, trustAnchorDigest)
	if err := supervisor.runOK(ctx, []string{"exec", "--user", "1000:1000", name, "node", "-e", trustProbe}); err != nil {
		return fmt.Errorf("verify enterprise trust: %w", err)
	}
	authCommand := "set -eu; umask 077; cat > /run/chora/pi/auth.json.tmp; chmod 0600 /run/chora/pi/auth.json.tmp; mv /run/chora/pi/auth.json.tmp /run/chora/pi/auth.json"
	if err := supervisor.runOKInput(ctx, []string{"exec", "--user", "1000:1000", "-i", name, "/bin/sh", "-c", authCommand}, projection.auth); err != nil {
		return fmt.Errorf("project Pi OAuth: %w", err)
	}
	authProbe := "const f=require('fs'),p='/run/chora/pi/auth.json',v=JSON.parse(f.readFileSync(p));if(Object.keys(v).length!==1||!v['openai-codex']||(f.statSync(p).mode&511)!==384)process.exit(2)"
	if err := supervisor.runOK(ctx, []string{"exec", "--user", "1000:1000", name, "node", "-e", authProbe}); err != nil {
		return fmt.Errorf("verify Pi OAuth projection: %w", err)
	}
	return nil
}

func (supervisor *Supervisor) runOKInput(ctx context.Context, args []string, input []byte) error {
	result, err := supervisor.config.Runner.Run(ctx, Command{Args: args, Stdin: bytes.NewReader(input)})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("docker exit %d: %s", result.ExitCode, boundedDiagnostic(result.Stderr))
	}
	return nil
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}
