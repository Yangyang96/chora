package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/pidiscovery"
	"github.com/Yangyang96/chora/internal/piinstall"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

// pathPiArgumentsPrefix is the exact argv prefix the Pi adapter emits for a
// PATH-discovered Pi Attempt before appending the per-Attempt --session-dir.
// It must stay in lock-step with the adapter's pathPiArguments + the
// --session-dir suffix appended in prepareStartForSource.
var pathPiArgumentsPrefix = []string{"--mode", "rpc", "--session-dir"}

// composePathPiRuntime builds the M2-S1 Local Connected Pi composition from a
// PATH-discovered, user-installed Pi. It deliberately carries no Docker,
// pidistribution, --repository-derived manager, or private-auth authority.
//
// A non-ready discovery result is not an error: the returned composition is
// empty (no adapter or supervisor), so the server still boots, GET
// /api/pi/discovery reports the state, and a Task start fails closed.
func composePathPiRuntime(ctx context.Context, runtimeRoot, sessionRoot string, discovery pidiscovery.Options, timeoutPolicy acceptanceTimeoutPolicy, installGuards ...piInstallationGuard) (piComposition, pidiscovery.Result, error) {
	result, err := pidiscovery.Discover(ctx, discovery)
	if err != nil {
		return piComposition{}, result, fmt.Errorf("discover PATH Pi: %w", err)
	}
	if result.State != pidiscovery.StateReady {
		return piComposition{}, result, nil
	}
	source, err := agentpi.NewPathPiSource(agentpi.PathPiSourceParams{
		ExecutablePath: result.ExecutablePath, Version: result.Version, ExecutableSHA256: result.ExecutableSHA256,
	})
	if err != nil {
		return piComposition{}, result, fmt.Errorf("bind PATH Pi source: %w", err)
	}
	executable, err := trustedhost.InspectExecutable(result.ExecutablePath)
	if err != nil {
		return piComposition{}, result, fmt.Errorf("inspect PATH Pi executable: %w", err)
	}
	helper, err := os.Executable()
	if err != nil {
		return piComposition{}, result, err
	}
	helper, err = filepath.EvalSymlinks(helper)
	if err != nil {
		return piComposition{}, result, err
	}
	observer, err := agentpi.InstallResourceObserver(filepath.Join(runtimeRoot, "resource-observer"), helper)
	if err != nil {
		return piComposition{}, result, fmt.Errorf("prepare read-only resource observer: %w", err)
	}
	adapter, err := agentpi.New(agentpi.Config{PathPiSource: source, SessionRoot: sessionRoot, ResourceObserver: &observer})
	if err != nil {
		return piComposition{}, result, fmt.Errorf("construct PATH Pi adapter: %w", err)
	}
	supervisorConfig := trustedhost.Config{
		RuntimeRoot:         filepath.Join(runtimeRoot, "pi-trusted"),
		AllowedAdapterID:    agentpi.AdapterID,
		AllowedExecutable:   executable,
		AllowedArguments:    append([]string(nil), pathPiArgumentsPrefix...),
		AllowedObserverPath: observer.ExtensionPath, ValidateObserver: observer.Validate,
		AllowedTrailingPathRoot: sessionRoot,
		DirectWorkingRoot:       true,
		MaxRuntime:              timeoutPolicy.AttemptTimeout,
	}
	if len(installGuards) > 0 && installGuards[0].installer != nil {
		guard := installGuards[0]
		supervisorConfig.RuntimeClosureIdentity = guard.identity()
		supervisorConfig.ValidateRuntimeClosure = guard.validate
	}
	trustedSupervisor, err := trustedhost.New(supervisorConfig)
	if err != nil {
		return piComposition{}, result, fmt.Errorf("activate Trusted Local PATH Pi execution: %w", err)
	}
	return piComposition{adapter: adapter, trustedSupervisor: trustedSupervisor}, result, nil
}

// A newly selected installation requires a fresh server composition. Checking
// at the supervisor boundary also covers API starts and automatic retries.
type piInstallationGuard struct {
	installer piinstall.Installer
	selection piinstall.Selection
}

func (g piInstallationGuard) identity() [32]byte {
	raw, _ := json.Marshal(g.selection)
	return sha256.Sum256(append([]byte("chora.pi-installation-guard.v1\x00"), raw...))
}
func (g piInstallationGuard) validate(ctx context.Context) ([32]byte, error) {
	state, err := g.installer.Inspect(ctx)
	if err != nil {
		return [32]byte{}, errors.New("Pi installation state is unavailable")
	}
	if state.Code == piinstall.StateInstalling || state.Code == piinstall.StateDrifted {
		return [32]byte{}, errors.New("Pi installation is in progress or has drifted")
	}
	selected, err := g.installer.ResolveSelection(ctx)
	if g.selection.ExecutablePath == "" {
		if state.SelectionPresent || state.Code == piinstall.StateInstalled || err == nil || !os.IsNotExist(err) {
			return [32]byte{}, errors.New("Pi installation changed; restart the server before starting another Task")
		}
	} else if err != nil || selected != g.selection {
		return [32]byte{}, errors.New("selected Pi installation changed after startup")
	}
	return g.identity(), nil
}
