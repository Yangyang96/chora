package dockersupervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/budgetchannel"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

const BudgetedPolicyDigest = "16842c1cd515037f9ce1ab15ffc2554182a9d415fe1f00531db7e2db8e33c56f"

// Only the app's compiled composition supplies this port in production. It
// never receives authority from a process frame. Recovery can revoke/stop but
// cannot reissue a channel or send a reserved model request.
type BudgetedAuthority interface {
	budgetchannel.Port
	Bind(context.Context, execution.Invocation) (domain.RestrictedLaunch, error)
	Admit(context.Context, domain.RestrictedLaunch) error
	Lease(context.Context, domain.RestrictedLaunch) error
	Recover(context.Context, domain.RestrictedLaunch) error
}
type BudgetedConfig struct {
	Identity    domain.RestrictedPiIdentity
	Permissions agentpi.BudgetedPermissionPlan
	Authority   BudgetedAuthority
}

func validBudgetedConfig(c Config) bool {
	b := c.Budgeted
	w := c.Workbench
	if b == nil || w == nil || b.Authority == nil || c.CredentialSource != "" || c.TrustAnchorSource != "" || w.Proxy.Enabled() || c.BoundaryImageID != "" || c.PolicyDigest != BudgetedPolicyDigest || b.Identity.Policy != BudgetedPolicyDigest || c.AttemptImageID != "sha256:"+b.Identity.Image || c.RuntimeSourceIdentity != b.Identity.RuntimeDigest() || agentpi.ValidateBudgetedStructure(b.Identity, b.Permissions) != nil {
		return false
	}
	a, _ := b.Permissions.Arguments()
	return slices.Equal(w.Arguments, a) && w.RuntimeVersion == b.Identity.PiVersion && w.HelperSHA256 == b.Identity.Helper && w.ObserverSHA256 == agentpi.ResourceObserverSHA256() && w.PrepareWorkspace != nil && w.CollectWorkspace != nil && w.RecoverWorkspace != nil && w.ReadOnlyWorkspacePaths != nil && validDigest(w.RuntimeFingerprint)
}
func (s *Supervisor) validateBudgetedInvocation(inv execution.Invocation, env map[string]string) (invocationMetadata, error) {
	b := s.config.Budgeted
	i := b.Identity
	w := s.config.Workbench
	if env[EnvExecutionProfile] != string(domain.AgentExecutionProfileIsolatedBudgeted) || env[EnvRuntimeSource] != domain.BudgetedPiRuntimeSource || env[EnvRuntimeSourceID] != i.RuntimeDigest() || env[EnvRuntimeVersion] != i.PiVersion || env[EnvCapabilityPolicy] != domain.BudgetedCapabilityPolicy || env[EnvExecutionProvider] != domain.DockerExecutionProvider || env[EnvPolicyDigest] != BudgetedPolicyDigest || env[EnvAttemptImageID] != s.config.AttemptImageID || env[EnvBoundaryImageID] != "" || env[EnvTrustDisclosure] != "" || env[EnvCapabilityBundleID] != "chora.public-pi-budgeted.v1" || env[EnvCapabilityBundleSHA] != w.ObserverSHA256 || env[EnvObserverSHA] != w.ObserverSHA256 || env[EnvObserverHelperSHA] != i.Helper || env[EnvObserverRuntimeSHA] != w.RuntimeFingerprint || env[EnvObserverConfigPath] != "/input/context/observer.json" || env[EnvObserverHelperPath] != agentpi.IsolatedHelperPath || !validDigest(env[EnvObserverConfigSHA]) {
		return invocationMetadata{}, errors.New("restricted invocation identity drift")
	}
	if digestCanonical(inv.Arguments()) != i.Arguments || digestCanonical(domain.RestrictedStableEnvironment(env)) != i.Environment {
		return invocationMetadata{}, errors.New("restricted final argv/config differs from authorization")
	}
	for k, v := range map[string]string{EnvCommandTimeout: "600", EnvAttemptTimeout: "1200", EnvPersistedLogLimit: "10485760", EnvArtifactLimit: "104857600", EnvResultPath: agentpi.ResultPath, EnvPatchPath: agentpi.PatchPath, EnvChecksPath: agentpi.ChecksPath, EnvRuntimeIdentityPath: agentpi.RuntimeIdentityPath} {
		if env[k] != v {
			return invocationMetadata{}, errors.New("restricted resource/output policy drift")
		}
	}
	prompt, task, err := validateWorkbenchPrompt(inv.Stdin(), env[EnvContextDigest], env[EnvContractDigest])
	if err != nil {
		return invocationMetadata{}, err
	}
	return invocationMetadata{runID: env[EnvRunID], attemptID: env[EnvAttemptID], taskID: task, prompt: prompt, environment: env}, nil
}

type budgetedRuntime struct {
	bound        domain.RestrictedLaunch
	guardian     Process
	cancel       context.CancelFunc
	leaseDone    chan struct{}
	leaseError   chan error
	guardianDone chan struct{}
	closeErr     error
	once         sync.Once
}

func (r *budgetedRuntime) close() error {
	r.once.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.guardian != nil {
			_ = r.guardian.Close()
			_ = r.guardian.Kill()
		}
		if r.leaseDone != nil {
			<-r.leaseDone
		}
		if r.guardianDone != nil {
			select {
			case <-r.guardianDone:
			case <-time.After(time.Second):
				r.closeErr = errors.New("guardian CLI exit unproven")
			}
		}
	})
	return r.closeErr
}

type guardianReady struct {
	once  sync.Once
	ready chan error
	data  []byte
	mu    sync.Mutex
}

func (g *guardianReady) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.data)+len(p) > 1024 {
		g.once.Do(func() { g.ready <- errors.New("oversize guardian readiness") })
		return 0, errors.New("oversize guardian readiness")
	}
	g.data = append(g.data, p...)
	if bytes.Contains(g.data, []byte{'\n'}) {
		want := []byte("{\"type\":\"guardian_ready\",\"schema\":\"chora.guardian.v1\"}\n")
		var err error
		if !bytes.Equal(g.data, want) {
			err = errors.New("guardian readiness identity mismatch")
		}
		g.once.Do(func() { g.ready <- err })
	}
	return len(p), nil
}

// The ordinary supervisor owns records, streams, terminal import, Stop, resource
// ownership and cleanup. This mode only replaces startup and the stdin framing.
func (s *Supervisor) setupBudgeted(ctx context.Context, r *attemptRecord, inv execution.Invocation, m invocationMetadata) error {
	b := r.budgeted.bound
	cfg := s.config.Budgeted
	if b.OutputCap < 1 || b.Validate() != nil || b.Run != m.runID || b.Attempt != m.attemptID || b.Task != m.taskID || b.LaunchToken != inv.LaunchToken().Value || b.SourceDigest != cfg.Identity.Digest() || !b.Deadline.Equal(inv.Deadline()) {
		return errors.New("restricted launch binding differs from invocation")
	}
	active := func() error {
		if !time.Now().Before(b.Deadline) || time.Now().Before(b.ObservedAt) {
			return errors.New("restricted deadline or clock regression")
		}
		return cfg.Authority.Admit(ctx, b)
	}
	if err := active(); err != nil {
		return err
	}
	paths := make([]string, 0, len(r.readOnlyWorkspaceMounts))
	mounts := map[string]string{}
	for _, v := range r.readOnlyWorkspaceMounts {
		paths = append(paths, filepath.ToSlash(v.destination[len("/workspace/repository/"):]))
		mounts[v.destination] = v.source
	}
	want := slices.Clone(cfg.Permissions.ReadOnlyPaths)
	sort.Strings(paths)
	sort.Strings(want)
	if !slices.Equal(paths, want) {
		return errors.New("permission_contract_unsupported: prepared resource masks differ")
	}
	observer, err := os.ReadFile(filepath.Join(r.contextDir, "observer.json"))
	if err != nil || digestText(observer) != m.environment[EnvObserverConfigSHA] {
		return errors.New("restricted observer config drift")
	}
	permissions, err := cfg.Permissions.Canonical()
	if err != nil {
		return err
	}
	if err = writeExclusive(filepath.Join(r.contextDir, "budgeted-permissions.json"), permissions); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(r.contextDir, "budgeted-permissions.json"), 0444); err != nil {
		return err
	}
	manifest := struct {
		Schema      string
		Bound       domain.RestrictedLaunch
		Identity    domain.RestrictedPiIdentity
		Permissions agentpi.BudgetedPermissionPlan
	}{"chora.budgeted-attempt.v1", b, cfg.Identity, cfg.Permissions}
	raw, _ := json.Marshal(manifest)
	if err = writeExclusive(filepath.Join(r.root, "budgeted.json"), raw); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(r.root, "budgeted.json"), 0400); err != nil {
		return err
	}
	labels := map[string]string{"chora.owner": "dockersupervisor", "chora.runtime_scope": s.recoveryScope, "chora.run_id": r.runID, "chora.attempt_id": r.attemptID, "chora.task_id": r.taskID, "chora.image_digest": s.config.AttemptImageID, "chora.policy_digest": BudgetedPolicyDigest}
	volume := []string{"volume", "create", "--driver", "local", "--opt", "type=tmpfs", "--opt", "device=tmpfs", "--opt", "o=size=256m,uid=1000,gid=1000,nosuid,nodev"}
	for _, k := range sortedKeys(labels) {
		volume = append(volume, "--label", k+"="+labels[k])
	}
	volume = append(volume, r.workspaceVolume)
	if err = active(); err != nil {
		return err
	}
	if err = s.runOK(ctx, volume); err != nil {
		return err
	}
	if err = s.verifyWorkbenchWorkspaceVolume(ctx, r, labels); err != nil {
		return err
	}
	plan := BudgetedPlan{Bound: b, ImageID: s.config.AttemptImageID, Container: r.container, WorkspaceVolume: r.workspaceVolume, ContextDirectory: r.contextDir, ReadOnlyMounts: mounts, Labels: labels}
	create, err := plan.Create(time.Now())
	if err != nil {
		return err
	}
	if err = active(); err != nil {
		return err
	}
	if err = s.runOK(ctx, create.Args); err != nil {
		return err
	}
	ready := &guardianReady{ready: make(chan error, 1)}
	guardian, err := s.config.Runner.Start(context.Background(), Command{Args: []string{"start", "-ai", r.container}, Stdout: ready, Stderr: io.Discard})
	r.budgeted.guardian = guardian
	if guardian != nil {
		r.budgeted.guardianDone = make(chan struct{})
		go func() { _, _ = guardian.Wait(); close(r.budgeted.guardianDone) }()
	}
	if err != nil || guardian == nil {
		return errors.Join(err, errors.New("guardian process ownership unavailable"))
	}
	leaseCtx, cancel := context.WithCancel(context.Background())
	r.budgeted.cancel = cancel
	r.budgeted.leaseDone = make(chan struct{})
	r.budgeted.leaseError = make(chan error, 1)
	go func() {
		defer close(r.budgeted.leaseDone)
		err := MaintainBudgetedLease(leaseCtx, guardian, b, cfg.Authority.Lease)
		if leaseCtx.Err() == nil {
			r.budgeted.leaseError <- err
		}
	}()
	readyCtx, readyCancel := context.WithTimeout(ctx, time.Second)
	defer readyCancel()
	select {
	case err = <-ready.ready:
		if err != nil {
			return err
		}
	case <-readyCtx.Done():
		return errors.New("guardian readiness unproven")
	}
	container, err := s.inspectEffectiveContainer(ctx, r.container)
	if err != nil {
		return err
	}
	actual, _ := json.Marshal(container)
	if err = plan.VerifyEffective(actual, container.ID, time.Now()); err != nil {
		return err
	}
	r.containerDockerID = container.ID
	workspace, err := archiveWorkbenchWorkspace(r.repository, r.readOnlyWorkspaceMounts)
	if err != nil {
		return err
	}
	if err = active(); err != nil {
		return err
	}
	if err = s.prepareWorkbenchMountParents(ctx, r); err != nil {
		return err
	}
	if err = s.runOKInput(ctx, []string{"exec", "--user", "1000:1000", "-i", r.container, "tar", "--extract", "--no-same-owner", "--no-overwrite-dir", "-C", "/workspace"}, workspace); err != nil {
		return err
	}
	if err = s.configureWorkbenchGitSafeDirectories(ctx, r); err != nil {
		return err
	}
	if err = active(); err != nil {
		return err
	}
	reader, writer := io.Pipe()
	args := []string{"exec", "-i", "--user", "1000:1000", "--workdir", "/workspace/repository"}
	for _, k := range sortedKeys(m.environment) {
		args = append(args, "--env", k+"="+m.environment[k])
	}
	args = append(args, "--env", "CHORA_BUDGETED_PERMISSION_SHA256="+cfg.Permissions.Digest(), "--env", "CHORA_BUDGETED_OUTPUT_CAP="+strconv.Itoa(b.OutputCap), r.container, "/usr/local/bin/node", BudgetedWrapperPath)
	args = append(args, inv.Arguments()...)
	process, err := s.config.Runner.Start(context.Background(), Command{Args: args, Stdout: writer, Stderr: &streamWriter{record: r, kind: execution.StreamStderr}})
	if process != nil {
		r.process = process
	}
	if err != nil || process == nil {
		_ = reader.Close()
		_ = writer.Close()
		return errors.Join(err, errors.New("restricted Pi process ownership unavailable"))
	}
	r.startReturned = true
	session, err := budgetchannel.New(&budgetedDuplex{Process: process, Reader: reader}, b, cfg.Authority)
	if err != nil {
		return err
	}
	sessionCtx, sessionCancel := context.WithDeadline(context.Background(), b.Deadline)
	framed := &budgetedProcess{Process: process, session: session, reader: reader, writer: writer, done: make(chan struct{}), cancel: sessionCancel}
	r.process = framed
	go func() {
		defer close(framed.done)
		if err := session.Receive(sessionCtx, &streamWriter{record: r, kind: execution.StreamStdout}); err != nil {
			_ = process.Kill()
		}
	}()
	return nil
}
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type budgetedProcess struct {
	Process
	session *budgetchannel.Session
	reader  *io.PipeReader
	writer  *io.PipeWriter
	done    chan struct{}
	cancel  context.CancelFunc
}

func (p *budgetedProcess) Write(raw []byte) (int, error) {
	if len(raw) > 2*budgetchannel.MaxPayload || len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return 0, errors.New("invalid bounded native RPC write")
	}
	for _, line := range bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'}) {
		if err := p.session.SendRPC(line); err != nil {
			return 0, err
		}
	}
	return len(raw), nil
}
func (p *budgetedProcess) Close() error { p.cancel(); _ = p.reader.Close(); return p.Process.Close() }
func (p *budgetedProcess) Kill() error  { p.cancel(); _ = p.reader.Close(); return p.Process.Kill() }
func (p *budgetedProcess) Wait() (int, error) {
	code, err := p.Process.Wait()
	p.cancel()
	_ = p.reader.Close()
	_ = p.writer.Close()
	<-p.done
	return code, err
}

// Called only during existing owned-resource recovery, after all containers
// have been proven absent. It never execs Pi or renews a lease.
func (s *Supervisor) recoverBudgeted(ctx context.Context, root string) error {
	manifest := filepath.Join(root, "budgeted.json")
	info, err := os.Lstat(manifest)
	if os.IsNotExist(err) && s.config.Budgeted == nil {
		return nil
	}
	stat, owned := markerInfoSys(info)
	if err != nil || !owned || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || stat.Nlink != 1 || info.Size() > 256*1024 {
		return errors.New("restricted recovery manifest missing or unsafe")
	}
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	var record struct {
		Schema      string
		Bound       domain.RestrictedLaunch
		Identity    domain.RestrictedPiIdentity
		Permissions agentpi.BudgetedPermissionPlan
	}
	if strictJSON(raw, &record) != nil || record.Schema != "chora.budgeted-attempt.v1" || record.Bound.Validate() != nil || agentpi.ValidateBudgetedStructure(record.Identity, record.Permissions) != nil || record.Bound.SourceDigest != record.Identity.Digest() {
		return fmt.Errorf("restricted recovery identity unproven")
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, canonical) {
		return errors.New("restricted recovery manifest is not canonical")
	}
	if s.config.Budgeted == nil || record.Identity != s.config.Budgeted.Identity {
		return errors.New("restricted recovery requires original source identity")
	}
	return s.config.Budgeted.Authority.Recover(ctx, record.Bound)
}

// Go attempts local cancellation independently of PID1 lease enforcement. A
// failed authority read cannot leave a wedged Pi RPC client running locally.
func (s *Supervisor) watchBudgetedLease(r *attemptRecord) {
	select {
	case <-r.done:
		return
	case <-r.budgeted.leaseError:
		_, _ = s.Stop(context.Background(), r.handle, execution.StopIntent{Kind: execution.StopForCancel, Reason: "restricted guardian lease lost"})
	}
}
