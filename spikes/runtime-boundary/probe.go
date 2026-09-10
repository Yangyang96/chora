package main

import (
	"context"
	"encoding/json"
	"flag"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Yangyang96/chora/internal/processbound"
	"github.com/Yangyang96/chora/internal/runtimeboundary"
)

func main() {
	var config runtimeboundary.Config
	flag.StringVar(&config.Workspace, "workspace", "", "permitted workspace")
	flag.StringVar(&config.RuntimeHomeFile, "runtime-home-file", "", "required runtime-home file")
	flag.StringVar(&config.ForbiddenRead, "forbidden-read", "", "forbidden Chora product-data file")
	flag.StringVar(&config.ControlPlane, "control-plane", "", "forbidden Chora control-plane binary")
	flag.StringVar(&config.LoopbackAddress, "loopback", "", "forbidden Chora loopback address")
	flag.StringVar(&config.OutsideWrite, "outside-write", "", "forbidden write target")
	flag.Parse()

	report := runtimeboundary.ProbeReport{}
	_, err := os.ReadFile(config.ForbiddenRead)
	report.ForbiddenReadDenied = err != nil
	controlContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err = processbound.Run(controlContext, processbound.Spec{
		Name:        config.ControlPlane,
		Args:        []string{"version"},
		StdoutLimit: 64 << 10,
		StderrLimit: 64 << 10,
	})
	cancel()
	report.ControlPlaneDenied = err != nil
	connection, err := net.DialTimeout("tcp4", config.LoopbackAddress, time.Second)
	if err == nil {
		_ = connection.Close()
	}
	report.LoopbackDenied = err != nil
	err = os.WriteFile(config.OutsideWrite, []byte("boundary probe"), 0o600)
	report.OutsideWriteDenied = err != nil
	workspaceFile := filepath.Join(config.Workspace, "probe-write.txt")
	err = os.WriteFile(workspaceFile, []byte("workspace"), 0o600)
	if err == nil {
		_, err = os.ReadFile(workspaceFile)
	}
	report.WorkspaceReadWriteAllowed = err == nil
	_, err = os.ReadFile(config.RuntimeHomeFile)
	report.RuntimeHomeReadAllowed = err == nil

	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		os.Exit(2)
	}
}
