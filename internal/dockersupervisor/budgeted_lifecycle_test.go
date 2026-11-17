package dockersupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

type budgetAuthorityFixture struct {
	mu                           sync.Mutex
	bound                        domain.RestrictedLaunch
	denied, stopped              bool
	requests, revoked, recovered int
	blocked                      chan struct{}
}

func (a *budgetAuthorityFixture) Bind(_ context.Context, inv execution.Invocation) (domain.RestrictedLaunch, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.bound
	b.LaunchToken = inv.LaunchToken().Value
	return b, nil
}
func (a *budgetAuthorityFixture) Admit(context.Context, domain.RestrictedLaunch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.denied || a.stopped {
		return errors.New("bound_unproven_or_stopped")
	}
	return nil
}
func (a *budgetAuthorityFixture) Lease(ctx context.Context, b domain.RestrictedLaunch) error {
	return a.Admit(ctx, b)
}
func (a *budgetAuthorityFixture) Prepare(context.Context, execution.StartRequest) error { return nil }
func (a *budgetAuthorityFixture) Dispatch(ctx context.Context, b domain.RestrictedLaunch, id string, raw []byte) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b.Channel != a.bound.Channel || a.stopped {
		return nil, errors.New("foreign or stopped")
	}
	a.requests++
	if a.blocked != nil {
		close(a.blocked)
		a.mu.Unlock()
		<-ctx.Done()
		a.mu.Lock()
		return nil, ctx.Err()
	}
	return json.RawMessage(`{"model":"deepseek-v4-pro"}`), nil
}
func (a *budgetAuthorityFixture) Revoke(context.Context, domain.RestrictedLaunch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoked++
	return nil
}
func (a *budgetAuthorityFixture) Recover(ctx context.Context, b domain.RestrictedLaunch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recovered++
	return nil
}

type budgetRunnerFixture struct {
	*fakeRunner
	badReady, badInspect bool
}

func (r *budgetRunnerFixture) Start(ctx context.Context, c Command) (Process, error) {
	p, err := r.fakeRunner.Start(ctx, c)
	if err == nil && len(c.Args) == 3 && slices.Equal(c.Args[:2], []string{"start", "-ai"}) {
		ready := "{\"type\":\"guardian_ready\",\"schema\":\"chora.guardian.v1\"}\n"
		if r.badReady {
			ready = "{\"type\":\"other\"}\n"
		}
		_, _ = c.Stdout.Write([]byte(ready))
	}
	return p, err
}
func (r *budgetRunnerFixture) Run(ctx context.Context, c Command) (CommandResult, error) {
	result, err := r.fakeRunner.Run(ctx, c)
	if r.badInspect && len(c.Args) == 5 && c.Args[0] == "container" && c.Args[3] == "{{json .}}" {
		var doc effectiveContainer
		_ = json.Unmarshal(result.Stdout, &doc)
		doc.HostConfig.NetworkMode = "bridge"
		result.Stdout, _ = json.Marshal(doc)
	}
	return result, err
}

func newBudgetedLifecycleFixture(t *testing.T) (*Supervisor, *budgetRunnerFixture, *budgetAuthorityFixture, execution.Invocation, *fakeProcess, *fakeProcess) {
	t.Helper()
	base := &fakeRunner{inspectMissing: true, exitOnRemove: true}
	runner := &budgetRunnerFixture{fakeRunner: base}
	guardian := newFakeProcess()
	pi := newFakeProcess()
	base.processes = []*fakeProcess{guardian, pi}
	var i domain.RestrictedPiIdentity
	v := reflect.ValueOf(&i).Elem()
	for n := 0; n < v.NumField(); n++ {
		v.Field(n).SetString(strings.Repeat("a", 64))
	}
	i.Schema = "chora.restricted-pi.v2"
	i.Source = domain.BudgetedPiRuntimeSource
	i.PiVersion = "0.85.1"
	i.NodeVersion = "synthetic"
	i.Endpoint = "deepseek.chat-completions.v1"
	i.CredentialScope = "synthetic-only"
	i.Image = strings.TrimPrefix(testAttemptImageID, "sha256:")
	i.Policy = BudgetedPolicyDigest
	p := agentpi.BudgetedPermissionPlan{Tools: []string{"read", "bash", "edit", "write"}, OriginalRequired: []agentpi.BudgetedExtension{}, Installed: []agentpi.BudgetedExtension{}, ReadOnlyPaths: []string{"repo/.git"}}
	d := agentpi.BudgetedAssetDigests()
	i.Shim = d["extension"]
	i.Serializer = d["provider"]
	i.Translator = d["provider"]
	i.Relay = d["relay"]
	i.Entry = agentpi.BudgetedEntryDigest()
	i.PermissionExtensions = p.Digest()
	i.AllowedTools = digestCanonical(p.Tools)
	args, _ := p.Arguments()
	i.Arguments = digestCanonical(args)
	root := t.TempDir()
	authority := &budgetAuthorityFixture{}
	config := Config{Runner: runner, RuntimeRoot: filepath.Join(root, "runtime"), ArtifactRoot: filepath.Join(root, "artifacts"), PolicyDigest: BudgetedPolicyDigest, AttemptImageID: testAttemptImageID, CapabilityContract: testCapabilityContractFor(t, testAttemptImageID, BudgetedPolicyDigest), EngineQualification: testEngineQualificationFor(t, testAttemptImageID, BudgetedPolicyDigest), CooperativeWait: time.Millisecond, DeathWait: 100 * time.Millisecond}
	config.Workbench = &WorkbenchConfig{Arguments: args, RuntimeVersion: i.PiVersion, ObserverSHA256: agentpi.ResourceObserverSHA256(), HelperSHA256: i.Helper, RuntimeFingerprint: strings.Repeat("b", 64), ReadOnlyWorkspacePaths: func(execution.Invocation) ([]string, error) { return []string{"repo/.git"}, nil }, RecoverWorkspace: func(context.Context, string) error { return nil }, CollectWorkspace: func(context.Context, execution.Invocation, string) error { return nil }, PrepareWorkspace: func(_ context.Context, _ execution.Invocation, repository string) error {
		if err := os.MkdirAll(filepath.Join(repository, "repo", ".git"), 0755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(repository)), "context", "observer.json"), []byte("{}\n"), 0444)
	}}
	baseInv := testWorkbenchInvocation(t, testSource(t), "budgeted-launch", config)
	env := baseInv.Environment()
	env[EnvExecutionProfile] = string(domain.AgentExecutionProfileIsolatedBudgeted)
	env[EnvRuntimeSource] = domain.BudgetedPiRuntimeSource
	env[EnvCapabilityPolicy] = domain.BudgetedCapabilityPolicy
	env[EnvCapabilityBundleID] = "chora.public-pi-budgeted.v1"
	env[EnvPolicyDigest] = BudgetedPolicyDigest
	i.Environment = digestCanonical(domain.RestrictedStableEnvironment(env))
	source, err := agentpi.NewBudgetedSource(i)
	if err != nil {
		t.Fatal(err)
	}
	fp := source.Fingerprint()
	config.Workbench.RuntimeFingerprint = hexDigest(fp.Digest[:])
	env[EnvObserverRuntimeSHA] = config.Workbench.RuntimeFingerprint
	env[EnvRuntimeSourceID] = i.RuntimeDigest()
	config.RuntimeSourceIdentity = i.RuntimeDigest()
	config.Budgeted = &BudgetedConfig{Identity: i, Permissions: p, Authority: authority}
	inv, err := execution.NewInvocation(execution.InvocationParams{AdapterID: "pi", Executable: "chora-pi-budgeted", WorkingRoot: baseInv.WorkingRoot(), Arguments: args, Environment: env, Stdin: baseInv.Stdin(), LaunchToken: baseInv.LaunchToken(), Target: baseInv.Target()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	inv = inv.WithDeadline(now.Add(time.Minute))
	authority.bound = domain.RestrictedLaunch{Schema: "chora.restricted-launch.v2", Authorization: "auth", Workflow: "workflow", Project: "project", Task: extractWorkbenchTask(t, inv), Charter: "charter", Run: env[EnvRunID], Attempt: env[EnvAttemptID], Actor: "owner", Gate: "gate", Operation: "op", InputDigest: strings.Repeat("a", 64), AuthorityDigest: strings.Repeat("a", 64), LaunchToken: inv.LaunchToken().Value, Channel: "channel", SourceDigest: i.Digest(), ConfigDigest: digestCanonical(env), Epoch: 1, OutputCap: 8, AuthorizedAt: now, ObservedAt: now, Deadline: inv.Deadline()}
	s, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return s, runner, authority, inv, guardian, pi
}
func hexDigest(b []byte) string { return fmt.Sprintf("%x", b) }
func extractWorkbenchTask(t *testing.T, inv execution.Invocation) string {
	t.Helper()
	_, task, err := validateWorkbenchPrompt(inv.Stdin(), inv.Environment()[EnvContextDigest], inv.Environment()[EnvContractDigest])
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestBudgetedLifecycleFramingLeaseStopAndRecovery(t *testing.T) {
	s, r, a, inv, guardian, pi := newBudgetedLifecycleFixture(t)
	out := s.Start(context.Background(), inv, &testSink{binding: inv.LaunchToken()})
	if out.Kind != execution.Started {
		t.Fatal(out)
	}
	pi.mu.Lock()
	input := pi.stdin.String()
	writer := pi.stdout
	pi.mu.Unlock()
	if !strings.Contains(input, `"type":"pi_rpc"`) {
		t.Fatal("raw RPC sent on outer provider channel", input)
	}
	_, err := writer.Write([]byte("{\"type\":\"dispatch_request\",\"id\":\"one\",\"payload\":{}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now().Add(time.Second)
	for {
		pi.mu.Lock()
		got := strings.Contains(pi.stdin.String(), `"type":"dispatch_result"`)
		pi.mu.Unlock()
		if got {
			break
		}
		if time.Now().After(end) {
			t.Fatal("missing framed provider reply")
		}
		time.Sleep(time.Millisecond)
	}
	a.mu.Lock()
	a.stopped = true
	a.mu.Unlock()
	stop, err := s.Stop(context.Background(), out.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stop.Kind != execution.StopConfirmed {
		t.Fatalf("stop=%+v err=%v", stop, err)
	}
	guardian.exit(0)
	waitDone(t, s.record(out.Handle).done)
	for _, c := range r.allCommands() {
		joined := strings.Join(c.Args, " ")
		if strings.Contains(joined, "auth.json") || strings.Contains(joined, "HTTP_PROXY") || strings.Contains(joined, "sleep infinity") {
			t.Fatal("legacy credential/network path used", joined)
		}
	}
	if err = s.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.requests != 1 || a.revoked != 1 {
		t.Fatalf("authority requests=%d revoked=%d", a.requests, a.revoked)
	}
}
func TestBudgetedLifecycleRefusesBeforePiOnProofReadinessOrInspectFault(t *testing.T) {
	for _, fault := range []string{"unproven", "ready", "inspect", "expired", "mask"} {
		t.Run(fault, func(t *testing.T) {
			s, r, a, inv, guardian, pi := newBudgetedLifecycleFixture(t)
			switch fault {
			case "unproven":
				a.denied = true
			case "ready":
				r.badReady = true
			case "inspect":
				r.badInspect = true
			case "expired":
				inv = inv.WithDeadline(time.Now().Add(-time.Second))
			case "mask":
				s.config.Workbench.ReadOnlyWorkspacePaths = func(execution.Invocation) ([]string, error) { return []string{"repo/.git", "other"}, nil }
			}
			out := s.Start(context.Background(), inv, &testSink{binding: inv.LaunchToken()})
			guardian.exit(0)
			pi.exit(0)
			if out.Kind == execution.Started {
				t.Fatal("fault released Pi", fault)
			}
			for _, c := range r.allCommands() {
				if slices.Contains(c.Args, BudgetedWrapperPath) {
					t.Fatal("Pi exec before proof/mask/ready/effective checks", fault)
				}
			}
			if fault == "unproven" || fault == "expired" {
				if len(r.allCommands()) != 0 {
					t.Fatal("closed preflight contacted Docker")
				}
			}
		})
	}
}

func TestBudgetedRecoveryRevokesOriginalChannelWithoutLaunchingOrRenewing(t *testing.T) {
	for _, fault := range []string{"normal", "expired", "missing", "writable", "symlink", "noncanonical", "foreign"} {
		t.Run(fault, func(t *testing.T) {
			s, r, a, _, _, _ := newBudgetedLifecycleFixture(t)
			root := t.TempDir()
			i := s.config.Budgeted.Identity
			b := a.bound
			if fault == "expired" {
				b.Deadline = time.Now().Add(-time.Second)
				b.AuthorizedAt = b.Deadline.Add(-time.Minute)
				b.ObservedAt = b.AuthorizedAt
			}
			if fault == "foreign" {
				i.Contract = strings.Repeat("d", 64)
				b.SourceDigest = i.Digest()
			}
			record := struct {
				Schema      string
				Bound       domain.RestrictedLaunch
				Identity    domain.RestrictedPiIdentity
				Permissions agentpi.BudgetedPermissionPlan
			}{"chora.budgeted-attempt.v1", b, i, s.config.Budgeted.Permissions}
			raw, _ := json.Marshal(record)
			if fault == "noncanonical" {
				raw = append(raw, '\n')
			}
			name := filepath.Join(root, "budgeted.json")
			if fault != "missing" {
				if err := os.WriteFile(name, raw, 0400); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "writable" {
				if err := os.Chmod(name, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "symlink" {
				if err := os.Rename(name, name+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(name+".real", name); err != nil {
					t.Fatal(err)
				}
			}
			err := s.recoverBudgeted(context.Background(), root)
			normal := fault == "normal" || fault == "expired"
			if (err == nil) != normal {
				t.Fatalf("recover %s: %v", fault, err)
			}
			a.mu.Lock()
			count := a.recovered
			requests := a.requests
			a.mu.Unlock()
			if (normal && count != 1) || (!normal && count != 0) || requests != 0 || len(r.allCommands()) != 0 {
				t.Fatalf("recovery side effects recovered=%d requests=%d commands=%d", count, requests, len(r.allCommands()))
			}
		})
	}
}

func TestBudgetedLifecycleAbsoluteDeadlineAndLeaseFailureForceLocalStop(t *testing.T) {
	for _, fault := range []string{"deadline", "lease"} {
		t.Run(fault, func(t *testing.T) {
			s, _, a, inv, _, _ := newBudgetedLifecycleFixture(t)
			if fault == "deadline" {
				inv = inv.WithDeadline(time.Now().Add(120 * time.Millisecond))
				a.bound.Deadline = inv.Deadline()
			}
			out := s.Start(context.Background(), inv, &testSink{binding: inv.LaunchToken()})
			if out.Kind != execution.Started {
				t.Fatal(out)
			}
			if fault == "lease" {
				a.mu.Lock()
				a.denied = true
				a.mu.Unlock()
			}
			select {
			case <-s.record(out.Handle).done:
			case <-time.After(4 * time.Second):
				t.Fatal("guardian/deadline did not stop local Pi")
			}
			got, err := s.Reconcile(context.Background(), out.Identity)
			if err != nil || got.Kind != execution.ReconcileDead {
				t.Fatalf("unproven death %+v %v", got, err)
			}
		})
	}
}

func TestBudgetedStopCancelsBlockedProviderWithoutWaitingForReply(t *testing.T) {
	s, _, a, inv, _, pi := newBudgetedLifecycleFixture(t)
	a.blocked = make(chan struct{})
	out := s.Start(context.Background(), inv, &testSink{binding: inv.LaunchToken()})
	if out.Kind != execution.Started {
		t.Fatal(out)
	}
	pi.mu.Lock()
	writer := pi.stdout
	pi.mu.Unlock()
	if _, err := writer.Write([]byte("{\"type\":\"dispatch_request\",\"id\":\"blocked\",\"payload\":{}}\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.blocked:
	case <-time.After(time.Second):
		t.Fatal("request not dispatched")
	}
	got, err := s.Stop(context.Background(), out.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || got.Kind != execution.StopConfirmed {
		t.Fatalf("stop=%+v err=%v", got, err)
	}
	waitDone(t, s.record(out.Handle).done)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.requests != 1 || a.revoked != 1 {
		t.Fatalf("requests=%d revoked=%d", a.requests, a.revoked)
	}
}
