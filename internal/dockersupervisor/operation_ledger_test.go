package dockersupervisor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

type ledgerTestRunner struct {
	mu         sync.Mutex
	result     CommandResult
	err        error
	processes  []*ledgerTestProcess
	runCalls   int
	startCalls int
}

func (runner *ledgerTestRunner) Run(context.Context, Command) (CommandResult, error) {
	runner.mu.Lock()
	runner.runCalls++
	runner.mu.Unlock()
	return runner.result, runner.err
}

func (runner *ledgerTestRunner) Start(context.Context, Command) (Process, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.startCalls++
	if len(runner.processes) == 0 {
		return nil, runner.err
	}
	process := runner.processes[0]
	runner.processes = runner.processes[1:]
	return process, runner.err
}

type ledgerTestProcess struct {
	exit int
	err  error
}

func (*ledgerTestProcess) Write(data []byte) (int, error) { return len(data), nil }
func (*ledgerTestProcess) Close() error                   { return nil }
func (*ledgerTestProcess) Kill() error                    { return nil }
func (process *ledgerTestProcess) Wait() (int, error)     { return process.exit, process.err }

func openTestLedger(t *testing.T, runner CommandRunner) *OperationLedger {
	t.Helper()
	root := filepath.Join(canonicalLedgerTestParent(t), "state")
	ledger, err := OpenOperationLedger(runner, root, "gen_test_123456789012345678901234")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	return ledger
}

func canonicalLedgerTestParent(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestOperationLedgerClassifiesWithoutPersistingSensitiveCommandData(t *testing.T) {
	ledger := openTestLedger(t, &ledgerTestRunner{})
	phase := RunnerForOperationPhase(ledger, OperationPhaseSetup)
	archiveDigest := strings.Repeat("a", 64)
	loadContext := WithOperationSafeTargetSHA256(context.Background(), archiveDigest)
	if _, err := phase.Run(loadContext, Command{Args: []string{"image", "load", "--quiet"}, Stdin: bytes.NewReader([]byte("private archive bytes"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := phase.Run(context.Background(), Command{Args: []string{"run", "--env", "TOKEN=secret", "/private/workspace"}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CommandAudit[0].OperationClass != "load" || snapshot.CommandAudit[0].SafeTargetSHA256 == nil || *snapshot.CommandAudit[0].SafeTargetSHA256 != archiveDigest || snapshot.CommandAudit[1].OperationClass != "other" || snapshot.CommandAudit[1].SafeTargetSHA256 != nil {
		t.Fatalf("unexpected classification: %+v", snapshot.CommandAudit)
	}
	bytes, err := os.ReadFile(ledger.filePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private archive bytes", "TOKEN", "secret", "/private/workspace"} {
		if strings.Contains(string(bytes), forbidden) {
			t.Fatalf("journal leaked %q: %s", forbidden, bytes)
		}
	}
}

func TestOperationLedgerRunSettlesSuccessFailureAndCancellation(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		run  *ledgerTestRunner
		want string
	}{
		{name: "success", ctx: context.Background(), run: &ledgerTestRunner{result: CommandResult{ExitCode: 0}}, want: "succeeded"},
		{name: "failure", ctx: context.Background(), run: &ledgerTestRunner{result: CommandResult{ExitCode: 7}, err: errors.New("exit")}, want: "failed"},
		{name: "cancelled", ctx: cancelledContext(), run: &ledgerTestRunner{result: CommandResult{ExitCode: -1}, err: context.Canceled}, want: "cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := openTestLedger(t, test.run)
			_, _ = RunnerForOperationPhase(ledger, OperationPhaseAttempt).Run(test.ctx, Command{Args: []string{"inspect", "x"}})
			snapshot, err := ledger.Snapshot()
			if err != nil || snapshot.CommandAudit[0].Result != test.want {
				t.Fatalf("Snapshot() = %+v, %v", snapshot, err)
			}
		})
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestOperationLedgerStartWaitSettlementPreservesReservationOrder(t *testing.T) {
	runner := &ledgerTestRunner{processes: []*ledgerTestProcess{{exit: 0}, {exit: 9, err: errors.New("exit")}}}
	ledger := openTestLedger(t, runner)
	phase := RunnerForOperationPhase(ledger, OperationPhaseAttempt)
	first, err := phase.Start(context.Background(), Command{Args: []string{"start", "first"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := phase.Start(context.Background(), Command{Args: []string{"exec", "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Snapshot(); err == nil {
		t.Fatal("Snapshot accepted pending Starts")
	}
	_, _ = second.Wait()
	_, _ = first.Wait()
	snapshot, err := ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CommandAudit[0].Sequence != 1 || snapshot.CommandAudit[0].Result != "succeeded" || snapshot.CommandAudit[1].Sequence != 2 || snapshot.CommandAudit[1].Result != "failed" {
		t.Fatalf("out-of-order settlement changed reservation order: %+v", snapshot.CommandAudit)
	}
}

func TestOperationLedgerStartCancellationAndProcessWithErrorFailClosed(t *testing.T) {
	cancelled := &ledgerTestRunner{processes: []*ledgerTestProcess{{exit: -1, err: context.Canceled}}}
	ledger := openTestLedger(t, cancelled)
	process, err := RunnerForOperationPhase(ledger, OperationPhaseAttempt).Start(cancelledContext(), Command{Args: []string{"start", "cancelled"}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = process.Wait()
	snapshot, err := ledger.Snapshot()
	if err != nil || snapshot.CommandAudit[0].Result != "cancelled" {
		t.Fatalf("cancelled Start = %+v, %v", snapshot, err)
	}

	returnedWithError := &ledgerTestRunner{processes: []*ledgerTestProcess{{exit: 1}}, err: errors.New("ambiguous start")}
	second := openTestLedger(t, returnedWithError)
	process, err = RunnerForOperationPhase(second, OperationPhaseAttempt).Start(context.Background(), Command{Args: []string{"start", "ambiguous"}})
	if err == nil || process != nil {
		t.Fatalf("ambiguous Start returned (%T, %v)", process, err)
	}
	snapshot, err = second.Snapshot()
	if err != nil || snapshot.CommandAudit[0].Result != "failed" {
		t.Fatalf("ambiguous Start snapshot = %+v, %v", snapshot, err)
	}
}

func TestOperationLedgerCrashReopenRetainsPessimisticCancellation(t *testing.T) {
	root := filepath.Join(canonicalLedgerTestParent(t), "state")
	ledger, err := OpenOperationLedger(&ledgerTestRunner{}, root, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.reserve(WithOperationPhase(context.Background(), OperationPhaseAttempt), "start", Command{Args: []string{"start", "x"}}); err != nil {
		t.Fatal(err)
	}
	// Simulate process death: the OS releases the lock without an orderly Close.
	_ = unix.Flock(int(ledger.lock.Fd()), unix.LOCK_UN)
	_ = ledger.lock.Close()
	ledger.closed = true
	reopened, err := OpenOperationLedger(&ledgerTestRunner{}, root, "g1")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot, err := reopened.Snapshot()
	if err != nil || snapshot.CommandAudit[0].Result != "cancelled" {
		t.Fatalf("crash snapshot = %+v, %v", snapshot, err)
	}
}

func TestOperationLedgerEnforcesOfflineImageAcquisitionPhases(t *testing.T) {
	phases := []OperationPhase{OperationPhaseSetup, OperationPhaseAttempt, OperationPhaseRetry, OperationPhaseRestart, OperationPhaseRecovery, OperationPhaseVerifier}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			underlying := &ledgerTestRunner{}
			ledger := openTestLedger(t, underlying)
			runner := RunnerForOperationPhase(ledger, phase)
			for _, args := range [][]string{{"build", "."}, {"buildx", "build", "."}, {"pull", "example.invalid/image"}, {"image", "pull"}} {
				if _, err := runner.Run(context.Background(), Command{Args: args}); err == nil {
					t.Fatalf("%s accepted forbidden %v", phase, args)
				}
			}
			loadContext := WithOperationSafeTargetSHA256(context.Background(), strings.Repeat("b", 64))
			_, loadErr := runner.Run(loadContext, Command{Args: []string{"image", "load", "--quiet"}, Stdin: bytes.NewReader([]byte("archive"))})
			if phase == OperationPhaseSetup && loadErr != nil {
				t.Fatalf("Setup load rejected: %v", loadErr)
			}
			if phase != OperationPhaseSetup && loadErr == nil {
				t.Fatalf("%s accepted load", phase)
			}
			snapshot, err := ledger.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			wantLoads := 0
			if phase == OperationPhaseSetup {
				wantLoads = 1
			}
			if len(snapshot.CommandAudit) != wantLoads || snapshot.PhaseOperationCounts[string(phase)].Build != 0 || snapshot.PhaseOperationCounts[string(phase)].Pull != 0 || snapshot.PhaseOperationCounts[string(phase)].Load != wantLoads || underlying.runCalls != wantLoads {
				t.Fatalf("%s enforcement = audit %+v counts %+v underlying calls %d", phase, snapshot.CommandAudit, snapshot.PhaseOperationCounts[string(phase)], underlying.runCalls)
			}
		})
	}

	ledger := openTestLedger(t, &ledgerTestRunner{})
	setup := RunnerForOperationPhase(ledger, OperationPhaseSetup)
	for _, command := range []Command{
		{Args: []string{"image", "load", "--quiet"}},
		{Args: []string{"image", "load", "--quiet"}, Stdin: bytes.NewReader([]byte("archive"))},
	} {
		if _, err := setup.Run(context.Background(), command); err == nil {
			t.Fatalf("Setup accepted unbound load: %+v", command.Args)
		}
	}
	loadContext := WithOperationSafeTargetSHA256(context.Background(), strings.Repeat("c", 64))
	if _, err := setup.Start(loadContext, Command{Args: []string{"image", "load", "--quiet"}, Stdin: bytes.NewReader([]byte("archive"))}); err == nil {
		t.Fatal("Setup accepted image load through Start")
	}
}

func TestOperationLedgerExclusiveLockSealCapAndCloseSemantics(t *testing.T) {
	root := filepath.Join(canonicalLedgerTestParent(t), "state")
	ledger, err := OpenOperationLedger(&ledgerTestRunner{}, root, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOperationLedger(&ledgerTestRunner{}, root, "g1"); err == nil {
		t.Fatal("concurrent ledger open succeeded")
	}
	processRunner := &ledgerTestRunner{processes: []*ledgerTestProcess{{exit: 0}}}
	ledger.runner = processRunner
	process, err := RunnerForOperationPhase(ledger, OperationPhaseAttempt).Start(context.Background(), Command{Args: []string{"start", "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err == nil {
		t.Fatal("Close accepted unsettled process")
	}
	_, _ = process.Wait()
	sealed, err := ledger.Seal()
	if err != nil || !sealed.AuditSealed {
		t.Fatalf("Seal() = %+v, %v", sealed, err)
	}
	if _, err := RunnerForOperationPhase(ledger, OperationPhaseAttempt).Run(context.Background(), Command{Args: []string{"inspect", "after"}}); err == nil {
		t.Fatal("sealed ledger accepted mutation")
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenOperationLedger(&ledgerTestRunner{}, root, "g1")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := RunnerForOperationPhase(reopened, OperationPhaseAttempt).Run(context.Background(), Command{Args: []string{"inspect", "after-reopen"}}); err == nil {
		t.Fatal("reopened sealed ledger accepted mutation")
	}

	capLedger := openTestLedger(t, &ledgerTestRunner{})
	capLedger.mu.Lock()
	capLedger.state.CommandAudit = make([]OperationRecord, maxOperationRecords)
	for index := range capLedger.state.CommandAudit {
		capLedger.state.CommandAudit[index] = OperationRecord{Phase: string(OperationPhaseRetry), Subsystem: "managed", Invocation: "run", OperationClass: "other", Result: "cancelled"}
	}
	capLedger.rechainLocked()
	err = capLedger.persistLocked()
	capLedger.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunnerForOperationPhase(capLedger, OperationPhaseRetry).Run(context.Background(), Command{Args: []string{"inspect"}}); err == nil {
		t.Fatal("full ledger accepted operation")
	}
}

func TestOperationLedgerPhaseBindings(t *testing.T) {
	ledger := openTestLedger(t, &ledgerTestRunner{})
	for _, phase := range []OperationPhase{OperationPhaseSetup, OperationPhaseAttempt, OperationPhaseRetry, OperationPhaseRestart, OperationPhaseRecovery, OperationPhaseVerifier} {
		if _, err := RunnerForOperationPhase(ledger, phase).Run(context.Background(), Command{Args: []string{"inspect", "x"}}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for index, phase := range []OperationPhase{OperationPhaseSetup, OperationPhaseAttempt, OperationPhaseRetry, OperationPhaseRestart, OperationPhaseRecovery, OperationPhaseVerifier} {
		wantSubsystem := phaseSubsystem(phase)
		if snapshot.CommandAudit[index].Phase != string(phase) || snapshot.CommandAudit[index].Subsystem != wantSubsystem {
			t.Fatalf("phase record %d = %+v", index, snapshot.CommandAudit[index])
		}
	}
}

func TestOperationLedgerRejectsSymlinkRootAndHardLinkedJournal(t *testing.T) {
	parent := canonicalLedgerTestParent(t)
	realRoot := filepath.Join(parent, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkRoot := filepath.Join(parent, "link")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOperationLedger(&ledgerTestRunner{}, symlinkRoot, "g1"); err == nil {
		t.Fatal("symlink state root was accepted")
	}
	realParent := filepath.Join(parent, "ancestor-real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	ancestorLink := filepath.Join(parent, "ancestor-link")
	if err := os.Symlink(realParent, ancestorLink); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOperationLedger(&ledgerTestRunner{}, filepath.Join(ancestorLink, "state"), "g1"); err == nil {
		t.Fatal("state root with a symlink ancestor was accepted")
	}

	root := filepath.Join(parent, "state")
	ledger, err := OpenOperationLedger(&ledgerTestRunner{}, root, "g1")
	if err != nil {
		t.Fatal(err)
	}
	journalPath := ledger.filePath
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(journalPath, journalPath+".hardlink"); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOperationLedger(&ledgerTestRunner{}, root, "g1"); err == nil {
		t.Fatal("hard-linked journal was accepted")
	}
}
