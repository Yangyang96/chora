package dockersupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/budgetchannel"
	"github.com/Yangyang96/chora/internal/domain"
)

// BudgetedPlan is a command plan, NOT an Engine/runtime qualification. It reuses
// the existing Runner/workspace authority, with a separate PID1 control pipe.
// Production selection stays unavailable until exact closure/permission,
// renderer/billing and independent Docker qualification are established.
type BudgetedPlan struct {
	Bound                                                 domain.RestrictedLaunch
	ImageID, Container, WorkspaceVolume, ContextDirectory string
	ReadOnlyMounts                                        map[string]string // destination -> approved host source
	Labels                                                map[string]string
}

const GuardianPath = "/opt/chora/chora-guardian"
const BudgetedWrapperPath = "/opt/chora/budgeted/wrapper.mjs"

func (p BudgetedPlan) Validate(now time.Time) error {
	if p.Bound.Validate() != nil || now.Before(p.Bound.ObservedAt) || !now.Before(p.Bound.Deadline) || !validImageID(p.ImageID) || !validText(p.Container, 128) || !validText(p.WorkspaceVolume, 128) || !validText(p.ContextDirectory, 4096) || strings.ContainsAny(p.ContextDirectory, ",\\") || p.ContextDirectory[0] != '/' || p.ReadOnlyMounts == nil {
		return errors.New("incomplete, expired or clock-regressed restricted plan")
	}
	for destination, source := range p.ReadOnlyMounts {
		if !validText(source, 4096) || strings.ContainsAny(source, ",\\") || source[0] != '/' || !validText(destination, 4096) || strings.ContainsAny(destination, ",\\") || !stringsHasWorkspacePrefix(destination) {
			return errors.New("invalid restricted read-only mount")
		}
	}
	return nil
}
func stringsHasWorkspacePrefix(s string) bool {
	return len(s) > len("/workspace/repository/") && s[:len("/workspace/repository/")] == "/workspace/repository/" && !containsDotDot(s)
}
func containsDotDot(s string) bool {
	for _, part := range strings.Split(s, "/") {
		if part == ".." || part == "." {
			return true
		}
	}
	return false
}

func (p BudgetedPlan) Create(now time.Time) (Command, error) {
	if err := p.Validate(now); err != nil {
		return Command{}, err
	}
	args := []string{"create", "--pull=never", "--name", p.Container, "--interactive", "--network", "none", "--pid", "private", "--ipc", "private", "--restart", "no", "--user", "0:0", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", "256", "--cpus", "2", "--memory", "4096m", "--memory-swap", "4096m", "--ulimit", "nofile=1024:1024", "--log-driver", "none", "--mount", "type=volume,src=" + p.WorkspaceVolume + ",dst=/workspace", "--mount", "type=bind,src=" + p.ContextDirectory + ",dst=/input/context,readonly", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=64m", "--tmpfs", "/run/chora/pi:rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=16m"}
	for _, k := range sortedKeys(p.Labels) {
		args = append(args, "--label", k+"="+p.Labels[k])
	}
	destinations := make([]string, 0, len(p.ReadOnlyMounts))
	for d := range p.ReadOnlyMounts {
		destinations = append(destinations, d)
	}
	slices.Sort(destinations)
	for _, d := range destinations {
		args = append(args, "--mount", "type=bind,src="+p.ReadOnlyMounts[d]+",dst="+d+",readonly")
	}
	args = append(args, "--entrypoint", GuardianPath, p.ImageID, p.Bound.AuthorizedAt.Format(time.RFC3339Nano), p.Bound.Deadline.Format(time.RFC3339Nano))
	return Command{Args: args}, nil
}

// AttachBudgetedProcess frames an already-owned Docker exec Process. The parent
// must verify guardian_ready, exact effective state and all authorization before
// it starts that Process. EOF cannot resume a channel or issue a new request.
func AttachBudgetedProcess(ctx context.Context, process Process, stdout io.Reader, rpc io.Writer, bound domain.RestrictedLaunch, port budgetchannel.Port, prompt json.RawMessage) error {
	s, err := budgetchannel.New(&budgetedDuplex{Process: process, Reader: stdout}, bound, port)
	if err != nil {
		return err
	}
	if err = s.SendRPC(prompt); err != nil {
		_ = process.Close()
		_ = port.Revoke(context.WithoutCancel(ctx), bound)
		return err
	}
	return s.Receive(ctx, rpc)
}

type budgetedDuplex struct {
	Process
	io.Reader
}

func (d *budgetedDuplex) Close() error {
	if closer, ok := d.Reader.(io.Closer); ok {
		_ = closer.Close()
	}
	return d.Process.Close()
}

// VerifyEffective is a required observation check, never a substitute for the
// independent Engine/image/permission qualification. Unknown fields relevant
// to networking, credentials, mounts and privilege are not silently accepted.
func (p BudgetedPlan) VerifyEffective(raw []byte, exactContainerID string, now time.Time) error {
	if err := p.Validate(now); err != nil {
		return err
	}
	var c effectiveContainer
	if json.Unmarshal(raw, &c) != nil || !domain.CanonicalSHA256(exactContainerID) || c.ID != exactContainerID || c.Image != p.ImageID || c.Name != "/"+p.Container {
		return errors.New("restricted container identity drift")
	}
	for k, v := range p.Labels {
		if c.Config.Labels[k] != v {
			return errors.New("restricted resource ownership drift")
		}
	}
	if !c.State.Running {
		return errors.New("restricted guardian is not running")
	}
	h := c.HostConfig
	if c.Config.User != "0:0" || !slices.Equal(c.Config.Entrypoint, []string{GuardianPath}) || !slices.Equal(c.Config.Cmd, []string{p.Bound.AuthorizedAt.Format(time.RFC3339Nano), p.Bound.Deadline.Format(time.RFC3339Nano)}) || h.NetworkMode != "none" || !exactNetworkMembership(c, []string{"none"}) || h.PidMode != "private" || h.IpcMode != "private" || h.RestartPolicy.Name != "no" || !h.ReadonlyRootfs || h.Privileged || len(h.CapAdd) != 0 || !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.SecurityOpt, []string{"no-new-privileges:true"}) || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 || len(h.PortBindings) != 0 || h.LogConfig.Type != "none" || h.NanoCPUs != 2_000_000_000 || h.Memory != 4096<<20 || h.MemorySwap != 4096<<20 || h.PidsLimit != 256 || !hasNofileLimit(c, 1024) {
		return errors.New("restricted effective privilege, guardian, network or resource drift")
	}
	if !slices.Equal(c.Config.Env, []string{"PATH=/usr/local/bin:/usr/bin:/bin"}) {
		return errors.New("restricted environment is not the fixed non-secret image environment")
	}
	want := map[string]effectiveMount{"/workspace": {Type: "volume", Source: p.WorkspaceVolume, RW: true}, "/input/context": {Type: "bind", Source: p.ContextDirectory, RW: false}}
	for d, s := range p.ReadOnlyMounts {
		want[d] = effectiveMount{Type: "bind", Source: s, RW: false}
	}
	if !exactMounts(c, want) || len(h.Tmpfs) != 2 || !tmpfsMatches(h.Tmpfs["/tmp"], []string{"rw", "nosuid", "nodev", "noexec", "uid=1000", "gid=1000", "size=64m"}) || !tmpfsMatches(h.Tmpfs["/run/chora/pi"], []string{"rw", "nosuid", "nodev", "noexec", "uid=1000", "gid=1000", "size=16m"}) {
		return errors.New("restricted mount, mask or tmpfs drift")
	}
	return nil
}

// MaintainBudgetedLease is separate from Pi RPC. Authority failure closes PID1
// stdin. PID1 enforces lease/deadline even if this Go process is forcibly lost.
// A nil error here is not death proof: exact Docker inspect is still required.
func MaintainBudgetedLease(ctx context.Context, control Process, b domain.RestrictedLaunch, authorize func(context.Context, domain.RestrictedLaunch) error) error {
	defer control.Close()
	if b.Validate() != nil || authorize == nil {
		return errors.New("invalid guardian lease binding")
	}
	last := time.Now()
	remaining := b.Deadline.Sub(last)
	if remaining <= 0 || last.Before(b.ObservedAt) {
		return errors.New("expired guardian lease")
	}
	absolute := time.NewTimer(remaining)
	defer absolute.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	sequence := uint64(0)
	for {
		now := time.Now()
		if now.Before(last) || !now.Before(b.Deadline) {
			return errors.New("guardian wall clock expired or regressed")
		}
		last = now
		if err := authorize(ctx, b); err != nil {
			return err
		}
		sequence++
		raw, _ := json.Marshal(struct {
			Sequence uint64 `json:"sequence"`
		}{sequence})
		raw = append(raw, '\n')
		if n, err := control.Write(raw); err != nil {
			return err
		} else if n != len(raw) {
			return io.ErrShortWrite
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-absolute.C:
			return errors.New("guardian absolute deadline")
		case <-ticker.C:
		}
	}
}
