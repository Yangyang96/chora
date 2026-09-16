package apppreview

import (
	"context"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
)

const (
	ProfileLocalConnected = "local_connected"
	ProfileIsolatedLocal  = "isolated_local"

	StateIdle             = "idle"
	StateStarting         = "starting"
	StateRunning          = "running"
	StateStopped          = "stopped"
	StateFailed           = "failed"
	StateRecoveryRequired = "recovery_required"
)

type Config struct {
	Command          string `json:"command"`
	WorkingDirectory string `json:"workingDirectory"`
	Port             int    `json:"port"`
}

type Target struct {
	Key       string `json:"key"`
	Root      string `json:"root"`
	AttemptID string `json:"attemptId"`
	Profile   string `json:"profile"`
}

type IsolatedRuntime struct {
	Runner         dockersupervisor.CommandRunner
	ImageID        string
	EngineIdentity string
}

type Options struct {
	RuntimeRoot     string
	LogCapacity     int64
	ResolveIsolated func(context.Context) (IsolatedRuntime, error)
}

type View struct {
	Key             string     `json:"key"`
	State           string     `json:"state"`
	Target          Target     `json:"target"`
	Config          Config     `json:"config"`
	URL             string     `json:"url,omitempty"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	ExitCode        *int       `json:"exitCode,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	CleanupRequired bool       `json:"cleanupRequired"`
	LogSize         int64      `json:"logSize"`
	LogTruncated    bool       `json:"logTruncated"`
}

type LogSlice struct {
	Data       []byte `json:"data"`
	NextOffset int64  `json:"nextOffset"`
	Size       int64  `json:"size"`
	Truncated  bool   `json:"truncated"`
}

type Suggestion struct {
	Name   string `json:"name"`
	Config Config `json:"config"`
}
