package piinstall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const selectionSchemaVersion = 1

type Core struct {
	dataRoot   string
	piRoot     string
	contract   Contract
	downloader Downloader
	toolchain  ToolchainProbe
	runner     CommandRunner
	readiness  ReadinessProbe
	clock      func() time.Time
	newID      func() string
	mu         sync.Mutex
	active     map[string]context.CancelFunc
}

type stateRecord struct {
	State
	IdempotencyHash string `json:"idempotencyHash,omitempty"`
}

type completion struct {
	SchemaVersion      int       `json:"schemaVersion"`
	ContractDigest     string    `json:"contractDigest"`
	LockSHA256         string    `json:"lockSha256"`
	ClosureDigest      string    `json:"closureDigest"`
	ExecutableRelative string    `json:"executableRelative"`
	ExecutableSHA256   string    `json:"executableSha256"`
	PiVersion          string    `json:"piVersion"`
	NodeVersion        string    `json:"nodeVersion"`
	NPMVersion         string    `json:"npmVersion"`
	OperationID        string    `json:"operationId"`
	CreatedAt          time.Time `json:"createdAt"`
}

type operationRecord struct {
	SchemaVersion   int        `json:"schemaVersion"`
	OperationID     string     `json:"operationId"`
	IdempotencyHash string     `json:"idempotencyHash"`
	ContractDigest  string     `json:"contractDigest"`
	State           StateCode  `json:"state"`
	Reason          ReasonCode `json:"reason,omitempty"`
	Phase           string     `json:"phase"`
	Message         string     `json:"message,omitempty"`
	StartedAt       time.Time  `json:"startedAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	ReusableStage   bool       `json:"reusableStage"`
}

type Error struct {
	Reason ReasonCode
	Err    error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func New(config Config) (*Core, error) {
	return newCore(config, FrozenContract(), true)
}

func newCore(config Config, contract Contract, enforceFrozen bool) (*Core, error) {
	if strings.TrimSpace(config.DataRoot) == "" {
		return nil, errors.New("Pi installer data root is required")
	}
	root, err := filepath.Abs(filepath.Clean(config.DataRoot))
	if err != nil || root == string(filepath.Separator) {
		return nil, errors.New("Pi installer data root is unsafe")
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	runner := config.CommandRunner
	if runner == nil {
		runner = standardCommandRunner{}
	}
	downloader := config.Downloader
	if downloader == nil {
		downloader = standardDownloader{}
	}
	probe := config.ToolchainProbe
	if probe == nil {
		probe = standardToolchainProbe{runner: runner}
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	newID := config.NewOperationID
	if newID == nil {
		newID = randomID
	}
	c := cloneContract(contract)
	if err := validateContract(c, enforceFrozen); err != nil {
		return nil, err
	}
	return &Core{dataRoot: root, piRoot: filepath.Join(root, "pi"), contract: c,
		downloader: downloader, toolchain: probe, runner: runner, readiness: config.ReadinessProbe,
		clock: clock, newID: newID, active: map[string]context.CancelFunc{}}, nil
}

func (c *Core) Inspect(ctx context.Context) (State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	record, err := c.loadState()
	if err != nil {
		if os.IsNotExist(err) {
			return State{Code: StateMissing, DestinationPath: c.piRoot, Source: c.contract.Package + "@" + c.contract.Version + " (" + c.contract.Registry + ")", UpdatedAt: c.clock()}, nil
		}
		return State{}, err
	}
	state := record.State
	state.DestinationPath = c.piRoot
	staleInstalling := false
	if state.Code == StateInstalling {
		c.mu.Lock()
		_, activeHere := c.active[state.OperationID]
		c.mu.Unlock()
		if !activeHere {
			if lock, lockErr := c.acquireLock(); lockErr == nil {
				lock.close()
				staleInstalling = true
			}
		}
	}
	if _, err := os.Stat(filepath.Join(c.piRoot, "selection.json")); err == nil {
		state.SelectionPresent = true
		selection, resolveErr := c.ResolveSelection(ctx)
		if resolveErr != nil {
			state.Code, state.Reason, state.Message = StateDrifted, ReasonSelection, "installed Pi selection failed identity validation"
		} else if staleInstalling || state.Code == StateFailed || state.Code == StateCancelled {
			state.Code, state.Reason, state.Message = StateInstalled, ReasonNone, ""
			state.Version, state.Source = selection.PiVersion, c.contract.Package+"@"+c.contract.Version+" ("+c.contract.Registry+")"
		}
	}
	return state, nil
}

func (c *Core) Install(ctx context.Context, request InstallRequest) (Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return Outcome{}, &Error{ReasonStateConflict, err}
	}
	if err := c.ensurePrivateRoot(); err != nil {
		return Outcome{}, &Error{ReasonPrivateRoot, err}
	}
	lock, err := c.acquireLock()
	if err != nil {
		return Outcome{}, &Error{ReasonInstallBusy, err}
	}
	defer lock.close()
	record, err := c.loadState()
	if err != nil && !os.IsNotExist(err) {
		return Outcome{}, err
	}
	keyHash := hexSHA256([]byte(request.IdempotencyKey))
	if record.IdempotencyHash == keyHash && record.Code == StateInstalled {
		selection, resolveErr := c.ResolveSelection(ctx)
		if resolveErr == nil {
			return Outcome{State: record.State, Selection: selection, RestartRequired: true}, nil
		}
	}
	if request.ExpectedStateVersion != record.StateVersion {
		return Outcome{}, &Error{ReasonStateConflict, fmt.Errorf("installer state version changed")}
	}
	operationID := c.newID()
	if !validOperationID(operationID) {
		return Outcome{}, &Error{ReasonUnexpected, errors.New("operation ID source returned an unsafe value")}
	}
	started := c.clock().UTC()
	installCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	c.mu.Lock()
	c.active[operationID] = cancel
	c.mu.Unlock()
	defer func() { cancel(); c.mu.Lock(); delete(c.active, operationID); c.mu.Unlock() }()
	state := State{Code: StateInstalling, StateVersion: record.StateVersion + 1, OperationID: operationID,
		DestinationPath: c.piRoot, Source: c.contract.Package + "@" + c.contract.Version + " (" + c.contract.Registry + ")", Version: c.contract.Version, UpdatedAt: started}
	if err := c.saveState(stateRecord{State: state, IdempotencyHash: keyHash}); err != nil {
		return Outcome{}, err
	}
	op := operationRecord{SchemaVersion: 1, OperationID: operationID, IdempotencyHash: keyHash, ContractDigest: c.contract.Digest,
		State: StateInstalling, Phase: "prerequisites", StartedAt: started, UpdatedAt: started}
	if err := c.saveOperation(op); err != nil {
		state.Code, state.Reason, state.Message, state.StateVersion = StateFailed, ReasonUnexpected, safeFailureMessage(ReasonUnexpected, "journal"), state.StateVersion+1
		_ = c.saveState(stateRecord{State: state, IdempotencyHash: keyHash})
		return Outcome{State: state}, &Error{ReasonUnexpected, err}
	}
	stagePath := ""
	fail := func(reason ReasonCode, phase string, cause error, reusable bool) (Outcome, error) {
		code := StateFailed
		message := safeFailureMessage(reason, phase)
		if reason == ReasonPrerequisite {
			code = StatePrerequisiteBlocked
		}
		if errors.Is(cause, context.Canceled) {
			code, reason, message = StateCancelled, ReasonCancelled, "installation cancelled"
		}
		now := c.clock().UTC()
		state.Code, state.Reason, state.Message, state.UpdatedAt = code, reason, message, now
		state.StateVersion++
		op.State, op.Reason, op.Phase, op.Message, op.UpdatedAt, op.ReusableStage = code, reason, phase, message, now, reusable
		_ = c.saveOperation(op)
		_ = c.saveState(stateRecord{State: state, IdempotencyHash: keyHash})
		if code == StateCancelled && stagePath != "" {
			_ = c.removeOwnedStage(stagePath, operationID)
		}
		return Outcome{State: state}, &Error{reason, cause}
	}
	toolchain, err := c.toolchain.Probe(installCtx)
	if err != nil {
		return fail(ReasonPrerequisite, "prerequisites", err, false)
	}
	if err := validateToolchain(toolchain, c.contract); err != nil {
		return fail(ReasonPrerequisite, "prerequisites", err, false)
	}
	stage := filepath.Join(c.piRoot, ".stage-"+operationID)
	stagePath = stage
	if err := c.createStage(stage, operationID); err != nil {
		return fail(ReasonPrivateRoot, "stage", err, false)
	}
	reusable := true
	tgz := filepath.Join(stage, "qualified-package.tgz")
	op.Phase, op.UpdatedAt, op.ReusableStage = "download", c.clock().UTC(), true
	if err := c.saveOperation(op); err != nil {
		return fail(ReasonUnexpected, "journal", err, false)
	}
	if err := c.downloader.Download(installCtx, DownloadRequest{URL: c.contract.TarballURL, DestinationPath: tgz, MaximumBytes: c.contract.MaxTarballBytes}); err != nil {
		return fail(ReasonDownload, "download", err, reusable)
	}
	if err := verifySRI(tgz, c.contract.TarballSRI, c.contract.MaxTarballBytes); err != nil {
		return fail(ReasonIntegrity, "integrity", err, false)
	}
	consumer := filepath.Join(stage, "consumer")
	if err := os.Mkdir(consumer, 0o700); err != nil {
		return fail(ReasonPrivateRoot, "consumer", err, reusable)
	}
	if err := writeExclusive(filepath.Join(consumer, "package.json"), c.contract.ConsumerManifest, 0o600); err != nil {
		return fail(ReasonPrivateRoot, "consumer", err, reusable)
	}
	if err := writeExclusive(filepath.Join(consumer, "package-lock.json"), c.contract.ConsumerLock, 0o600); err != nil {
		return fail(ReasonPrivateRoot, "consumer", err, reusable)
	}
	userconfig := filepath.Join(c.piRoot, "empty-npmrc")
	if err := ensureEmptyFile(userconfig); err != nil {
		return fail(ReasonPrivateRoot, "consumer", err, reusable)
	}
	globalconfig := filepath.Join(c.piRoot, "empty-global-npmrc")
	if err := ensureEmptyFile(globalconfig); err != nil {
		return fail(ReasonPrivateRoot, "consumer", err, reusable)
	}
	cache := filepath.Join(c.piRoot, "npm-cache")
	if err := ensurePrivateDir(cache); err != nil {
		return fail(ReasonPrivateRoot, "consumer", err, reusable)
	}
	home := filepath.Join(stage, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		return fail(ReasonPrivateRoot, "consumer", err, reusable)
	}
	temporary := filepath.Join(stage, "tmp")
	if err := os.Mkdir(temporary, 0o700); err != nil {
		return fail(ReasonPrivateRoot, "consumer", err, reusable)
	}
	args := materializeArgs(c.contract.NPMArguments, consumer, cache, userconfig)
	command := Command{Executable: toolchain.NPMExecutable, Arguments: args, Directory: consumer,
		Environment: []string{"HOME=" + home, "TMPDIR=" + temporary, "PATH=" + filepath.Dir(toolchain.NodeExecutable), "NO_COLOR=1"}, OutputLimit: 32 << 10}
	op.Phase, op.UpdatedAt = "npm_ci", c.clock().UTC()
	if err := c.saveOperation(op); err != nil {
		return fail(ReasonUnexpected, "journal", err, reusable)
	}
	result, err := c.runner.Run(installCtx, command)
	if err != nil {
		return fail(ReasonNPM, "npm_ci", err, reusable)
	}
	if result.ExitCode != 0 {
		return fail(ReasonNPM, "npm_ci", fmt.Errorf("npm ci exited with status %d", result.ExitCode), reusable)
	}
	installedManifest, manifestErr := readRegularNoFollow(filepath.Join(consumer, "package.json"), 1<<20)
	installedLock, lockErr := readRegularNoFollow(filepath.Join(consumer, "package-lock.json"), 8<<20)
	if manifestErr != nil || lockErr != nil || hexSHA256(installedManifest) != hexSHA256(c.contract.ConsumerManifest) || hexSHA256(installedLock) != c.contract.ConsumerLockSHA256 {
		return fail(ReasonLockDrift, "validate_lock", errors.New("npm changed the frozen consumer manifest or lock"), reusable)
	}
	closureDigest, executableRelative, executableHash, err := validateInstalledClosure(consumer, c.contract)
	if err != nil {
		return fail(ReasonClosure, "validate_closure", err, reusable)
	}
	stagedExecutable := filepath.Join(stage, filepath.FromSlash(executableRelative))
	versionResult, err := c.runner.Run(installCtx, Command{Executable: stagedExecutable, Arguments: []string{"--version"}, Directory: consumer, Environment: command.Environment, OutputLimit: 4096})
	if err != nil || versionResult.ExitCode != 0 || strings.TrimSpace(versionResult.Stdout) != c.contract.Version {
		if err == nil {
			err = fmt.Errorf("Pi reported version %q", strings.TrimSpace(versionResult.Stdout))
		}
		return fail(ReasonVersion, "validate_version", err, reusable)
	}
	configured := false
	if c.readiness != nil {
		ready, probeErr := c.readiness.Probe(installCtx, stagedExecutable)
		if probeErr == nil {
			configured = ready.Configured || len(ready.ReadyProviders) > 0
		}
	}
	complete := completion{SchemaVersion: 1, ContractDigest: c.contract.Digest, LockSHA256: c.contract.ConsumerLockSHA256,
		ClosureDigest: closureDigest, ExecutableRelative: executableRelative, ExecutableSHA256: executableHash,
		PiVersion: c.contract.Version, NodeVersion: toolchain.NodeVersion, NPMVersion: toolchain.NPMVersion,
		OperationID: operationID, CreatedAt: c.clock().UTC()}
	completeBytes, _ := json.MarshalIndent(complete, "", "  ")
	completeBytes = append(completeBytes, '\n')
	if err := os.Remove(tgz); err != nil {
		return fail(ReasonPrivateRoot, "publish", err, reusable)
	}
	if err := os.RemoveAll(home); err != nil {
		return fail(ReasonPrivateRoot, "publish", err, reusable)
	}
	if err := os.RemoveAll(temporary); err != nil {
		return fail(ReasonPrivateRoot, "publish", err, reusable)
	}
	if err := writeExclusive(filepath.Join(stage, ".complete"), completeBytes, 0o600); err != nil {
		return fail(ReasonPrivateRoot, "publish", err, reusable)
	}
	if err := syncTree(stage); err != nil {
		return fail(ReasonPrivateRoot, "publish", err, reusable)
	}
	finalRoot := filepath.Join(c.piRoot, c.contract.Digest)
	if err := c.publishStage(stage, finalRoot, complete); err != nil {
		return fail(ReasonClosure, "publish", err, reusable)
	}
	selection := Selection{SchemaVersion: selectionSchemaVersion, ContractDigest: c.contract.Digest,
		LockSHA256: c.contract.ConsumerLockSHA256, ClosureDigest: closureDigest, ContentID: c.contract.Digest,
		ExecutableRelative: executableRelative, ExecutablePath: filepath.Join(finalRoot, filepath.FromSlash(executableRelative)),
		ExecutableSHA256: executableHash, PiVersion: c.contract.Version, NodeVersion: toolchain.NodeVersion,
		NPMVersion: toolchain.NPMVersion, OperationID: operationID, CreatedAt: complete.CreatedAt}
	if err := c.validateSelection(installCtx, selection); err != nil {
		return fail(ReasonSelection, "selection", err, true)
	}
	if err := atomicJSON(filepath.Join(c.piRoot, "selection.json"), selection, 0o600); err != nil {
		return fail(ReasonSelection, "selection", err, true)
	}
	now := c.clock().UTC()
	state.Code, state.Reason, state.Message, state.StateVersion, state.UpdatedAt = StateInstalled, ReasonNone, "", state.StateVersion+1, now
	state.SelectionPresent, state.Configured = true, configured
	if !configured {
		state.ConfigurationAction = ConfigurationCommand(selection.ExecutablePath)
	}
	op.State, op.Reason, op.Phase, op.Message, op.UpdatedAt, op.ReusableStage = StateInstalled, ReasonNone, "complete", "", now, false
	if err := c.saveOperation(op); err != nil {
		return fail(ReasonUnexpected, "complete", err, false)
	}
	if err := c.saveState(stateRecord{State: state, IdempotencyHash: keyHash}); err != nil {
		return Outcome{}, err
	}
	return Outcome{State: state, Selection: selection, RestartRequired: true}, nil
}

func (c *Core) Cancel(ctx context.Context, operationID string) error {
	if !validOperationID(operationID) {
		return &Error{ReasonStateConflict, errors.New("invalid operation ID")}
	}
	c.mu.Lock()
	cancel := c.active[operationID]
	c.mu.Unlock()
	if cancel == nil {
		return &Error{ReasonStateConflict, errors.New("operation is not active in this process")}
	}
	cancel()
	return nil
}

func (c *Core) ResolveSelection(ctx context.Context) (Selection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := readRegularNoFollow(filepath.Join(c.piRoot, "selection.json"), 64<<10)
	if err != nil {
		return Selection{}, err
	}
	var selection Selection
	if err := json.Unmarshal(data, &selection); err != nil {
		return Selection{}, &Error{ReasonSelection, errors.New("selection JSON is invalid")}
	}
	if err := c.validateSelection(ctx, selection); err != nil {
		return Selection{}, &Error{ReasonSelection, err}
	}
	return selection, nil
}

func (c *Core) validateSelection(ctx context.Context, selection Selection) error {
	if selection.SchemaVersion != selectionSchemaVersion || selection.ContractDigest != c.contract.Digest || selection.LockSHA256 != c.contract.ConsumerLockSHA256 || selection.ContentID != c.contract.Digest || selection.PiVersion != c.contract.Version {
		return errors.New("selection identity differs from frozen contract")
	}
	root := filepath.Join(c.piRoot, selection.ContentID)
	wantPath := filepath.Join(root, filepath.FromSlash(selection.ExecutableRelative))
	if selection.ExecutableRelative == "" || selection.ExecutablePath != wantPath || !inside(root, wantPath) {
		return errors.New("selection executable path is outside content root")
	}
	resolved, err := filepath.EvalSymlinks(wantPath)
	if err != nil || resolved != wantPath || !inside(root, resolved) {
		return errors.New("selection executable cannot be resolved safely")
	}
	hash, err := validateExecutable(resolved)
	if err != nil || hash != selection.ExecutableSHA256 {
		return errors.New("selection executable identity drifted")
	}
	closure, _, _, err := validateInstalledClosure(filepath.Join(root, "consumer"), c.contract)
	if err != nil || closure != selection.ClosureDigest {
		return errors.New("selection closure identity drifted")
	}
	version, err := c.runner.Run(ctx, Command{Executable: resolved, Arguments: []string{"--version"}, Directory: filepath.Join(root, "consumer"), OutputLimit: 4096})
	if err != nil || version.ExitCode != 0 || strings.TrimSpace(version.Stdout) != c.contract.Version {
		return errors.New("selected Pi version drifted")
	}
	return nil
}

func materializeArgs(template []string, consumer, cache, userconfig string) []string {
	args := make([]string, len(template))
	for i, value := range template {
		switch value {
		case "{consumer}":
			args[i] = consumer
		case "{cache}":
			args[i] = cache
		case "{userconfig}":
			args[i] = userconfig
		case "{globalconfig}":
			args[i] = filepath.Join(filepath.Dir(userconfig), "empty-global-npmrc")
		default:
			args[i] = value
		}
	}
	return args
}

func validateIdempotencyKey(key string) error {
	if len(key) < 8 || len(key) > 128 || strings.TrimSpace(key) != key {
		return errors.New("idempotency key must contain 8-128 bounded characters")
	}
	for _, r := range key {
		if r < 0x21 || r > 0x7e {
			return errors.New("idempotency key contains unsupported characters")
		}
	}
	return nil
}

var operationPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$`)

func validOperationID(value string) bool { return operationPattern.MatchString(value) }

func randomID() string {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "invalid-operation-id"
	}
	return hex.EncodeToString(data[:])
}

func safeFailureMessage(reason ReasonCode, phase string) string {
	if len(phase) > 48 {
		phase = "operation"
	}
	return fmt.Sprintf("Pi installation stopped during %s (%s)", phase, reason)
}

// ConfigurationCommand launches the exact selected installation without trusting PATH.
// Authentication and model selection remain native interactive Pi commands.
func ConfigurationCommand(executable string) string {
	return "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
}
