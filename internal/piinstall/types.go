// Package piinstall installs one frozen Pi distribution into a Chora-owned
// private data root. It is deliberately not a general package manager.
package piinstall

import (
	"context"
	"time"
)

type StateCode string

const (
	StateMissing             StateCode = "missing"
	StatePrerequisiteBlocked StateCode = "prerequisite_blocked"
	StateInstalling          StateCode = "installing"
	StateInstalled           StateCode = "installed"
	StateFailed              StateCode = "failed"
	StateCancelled           StateCode = "cancelled"
	StateDrifted             StateCode = "drifted"
)

type ReasonCode string

const (
	ReasonNone          ReasonCode = ""
	ReasonStateConflict ReasonCode = "state_conflict"
	ReasonInstallBusy   ReasonCode = "install_busy"
	ReasonPrerequisite  ReasonCode = "prerequisite"
	ReasonPrivateRoot   ReasonCode = "private_root"
	ReasonDownload      ReasonCode = "download_failed"
	ReasonIntegrity     ReasonCode = "integrity_mismatch"
	ReasonNPM           ReasonCode = "npm_failed"
	ReasonLockDrift     ReasonCode = "consumer_lock_drift"
	ReasonClosure       ReasonCode = "closure_invalid"
	ReasonExecutable    ReasonCode = "executable_invalid"
	ReasonVersion       ReasonCode = "version_mismatch"
	ReasonSelection     ReasonCode = "selection_invalid"
	ReasonCancelled     ReasonCode = "cancelled"
	ReasonUnexpected    ReasonCode = "unexpected_failure"
)

type State struct {
	Code                StateCode  `json:"code"`
	Reason              ReasonCode `json:"reason,omitempty"`
	Message             string     `json:"message,omitempty"`
	StateVersion        uint64     `json:"stateVersion"`
	OperationID         string     `json:"operationId,omitempty"`
	DestinationPath     string     `json:"destinationPath"`
	Version             string     `json:"version,omitempty"`
	Source              string     `json:"source,omitempty"`
	SelectionPresent    bool       `json:"selectionPresent"`
	Configured          bool       `json:"configured"`
	ConfigurationAction string     `json:"configurationAction,omitempty"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

type InstallRequest struct {
	IdempotencyKey       string
	ExpectedStateVersion uint64
}

type Outcome struct {
	State           State
	Selection       Selection
	RestartRequired bool
}

type Selection struct {
	SchemaVersion      int       `json:"schemaVersion"`
	ContractDigest     string    `json:"contractDigest"`
	LockSHA256         string    `json:"lockSha256"`
	ClosureDigest      string    `json:"closureDigest"`
	ContentID          string    `json:"contentId"`
	ExecutableRelative string    `json:"executableRelative"`
	ExecutablePath     string    `json:"executablePath"`
	ExecutableSHA256   string    `json:"executableSha256"`
	PiVersion          string    `json:"piVersion"`
	NodeVersion        string    `json:"nodeVersion"`
	NPMVersion         string    `json:"npmVersion"`
	OperationID        string    `json:"operationId"`
	CreatedAt          time.Time `json:"createdAt"`
}

type Contract struct {
	Package             string
	Version             string
	Registry            string
	TarballURL          string
	TarballSRI          string
	ConsumerManifest    []byte
	ConsumerLock        []byte
	ConsumerLockSHA256  string
	ExecutableRelative  string
	MinimumNodeVersion  string
	MinimumNPMMajor     int
	MaxTarballBytes     int64
	NPMArguments        []string
	LifecycleScriptsOff bool
	Digest              string
}

type DownloadRequest struct {
	URL             string
	DestinationPath string
	MaximumBytes    int64
}

type Downloader interface {
	Download(context.Context, DownloadRequest) error
}

type Toolchain struct {
	NodeExecutable string
	NodeVersion    string
	NPMExecutable  string
	NPMVersion     string
}

type ToolchainProbe interface {
	Probe(context.Context) (Toolchain, error)
}

type Command struct {
	Executable  string
	Arguments   []string
	Directory   string
	Environment []string
	OutputLimit int
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type CommandRunner interface {
	Run(context.Context, Command) (CommandResult, error)
}

type Readiness struct {
	Configured     bool
	ReadyProviders []string
}

type ReadinessProbe interface {
	Probe(context.Context, string) (Readiness, error)
}

type Config struct {
	DataRoot       string
	Downloader     Downloader
	ToolchainProbe ToolchainProbe
	CommandRunner  CommandRunner
	ReadinessProbe ReadinessProbe
	Clock          func() time.Time
	NewOperationID func() string
}

type Installer interface {
	Inspect(context.Context) (State, error)
	Install(context.Context, InstallRequest) (Outcome, error)
	Cancel(context.Context, string) error
	ResolveSelection(context.Context) (Selection, error)
}
