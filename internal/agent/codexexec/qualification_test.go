package codexexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

func evidenceFor(t *testing.T, r *execution.QualificationReport, capability string) execution.CapabilityEvidence {
	t.Helper()
	if r != nil {
		for _, e := range r.Evidence {
			if e.Capability == capability {
				return e
			}
		}
	}
	t.Fatal("missing capability diagnostic", capability)
	return execution.CapabilityEvidence{}
}

func TestProtocolGenerationMatrixAndPublicRefusal(t *testing.T) {
	for _, version := range []string{"codex-cli 0.155.1", "codex-cli 0.167.2"} {
		t.Run(version, func(t *testing.T) {
			a := versionFixture(t, version, "exit 0\n")
			fp, err := a.Fingerprint(context.Background())
			if err != nil || fp.Version != version {
				t.Fatal("actual version not frozen", fp, err)
			}
			ready := Inspect(context.Background(), a.config)
			if ready.Ready || ready.Failure != ErrManagedPolicy.Error() || ready.Qualification == nil || ready.Qualification.State != execution.QualificationRejected {
				t.Fatal(ready)
			}
			if ready.Qualification.Identity.Version != version || evidenceFor(t, ready.Qualification, "launch_arguments").Method != "native_help" || evidenceFor(t, ready.Qualification, "event_frames").State != execution.QualificationVerified || evidenceFor(t, ready.Qualification, "configuration_sources").State != execution.QualificationRejected {
				t.Fatal(ready.Qualification)
			}
		})
	}
}

func TestUnknownAndMissingCapabilitiesNeverProbeLoginOrExec(t *testing.T) {
	for _, mode := range []string{"unknown_version", "missing_flag", "misleading_flag"} {
		t.Run(mode, func(t *testing.T) {
			a := fixture(t, "exit 0\n")
			marker := filepath.Join(filepath.Dir(a.config.Home), "unexpected")
			version, help := "codex-cli 0.167.2", fixtureHelp
			want := execution.QualificationRejected
			if mode == "unknown_version" {
				version, want = "codex-cli 0.999.0", execution.QualificationPending
			}
			if mode == "missing_flag" {
				help = strings.ReplaceAll(help, "--ignore-user-config", "")
			}
			if mode == "misleading_flag" {
				help = strings.ReplaceAll(help, "--ignore-user-config", "--ignore-user-config-unsafe")
			}
			script := "#!/bin/sh\ncase \"$*\" in\n--version) echo '" + version + "';;\n'exec --help') echo '" + help + "';;\n*) touch '" + marker + "'; exit 1;;\nesac\n"
			if err := os.WriteFile(a.config.Executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ready := Inspect(context.Background(), a.config)
			if ready.Ready || ready.Qualification == nil || ready.Qualification.State != want {
				t.Fatal(ready)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("qualification caused login/exec side effect", err)
			}
			if mode != "unknown_version" && evidenceFor(t, ready.Qualification, "launch_arguments").Reference != "missing_flag:--ignore-user-config" {
				t.Fatal(ready.Qualification)
			}
		})
	}
}

func TestExecutableCannotChangeBetweenNativeProbes(t *testing.T) {
	a := fixture(t, "exit 0\n")
	script := "#!/bin/sh\ncase \"$*\" in\n--version) echo 'codex-cli 0.155.1';;\n'exec --help') echo '" + fixtureHelp + "'; echo '# replaced' >> \"$0\";;\nesac\n"
	if err := os.WriteFile(a.config.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), a.config); !errors.Is(err, ErrIdentity) {
		t.Fatal("mixed-binary evidence accepted", err)
	}
}

func TestFrozenQualificationRejectsUpgradeReplacementAndConfigDrift(t *testing.T) {
	for _, mode := range []string{"same_version_replacement", "upgrade", "configuration", "security"} {
		t.Run(mode, func(t *testing.T) {
			a := fixture(t, "exit 0\n")
			req := request(t, a)
			prepared, err := a.PrepareBoundStart(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			s, err := trustedhost.New(a.SupervisorConfig(filepath.Join(t.TempDir(), "runtime")))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "same_version_replacement", "upgrade":
				raw, err := os.ReadFile(a.config.Executable)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "upgrade" {
					raw = []byte(strings.ReplaceAll(string(raw), "0.155.1", "0.167.2"))
				} else {
					raw = append(raw, []byte("# different content, same displayed version\n")...)
				}
				if err := os.WriteFile(a.config.Executable, raw, 0700); err != nil {
					t.Fatal(err)
				}
			case "configuration":
				a.config.Model = "different-model"
			case "security":
				a.managedPolicyCheck = func() error { return errors.New("policy epoch changed") }
			}
			if _, err := a.PrepareBoundStart(context.Background(), req); err == nil {
				t.Fatal("frozen attempt accepted drift")
			}
			if outcome := s.Start(context.Background(), prepared.Invocation, &sink{token: req.LaunchToken, done: make(chan struct{})}); outcome.Kind != execution.StartProvenNoChild {
				t.Fatal("drifted prepared invocation spawned", outcome)
			}
			if _, err := a.PrepareResume(context.Background(), execution.ResumeRequest{}); !errors.Is(err, execution.ErrUnsupportedCapability) {
				t.Fatal("resume accepted", err)
			}
			if mode == "upgrade" {
				fresh, err := newAdapter(context.Background(), a.config, func() error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				if fresh.fingerprint.Version != "codex-cli 0.167.2" || fresh.fingerprint.Digest == a.fingerprint.Digest {
					t.Fatal("new version reused old fingerprint")
				}
				if _, err := fresh.PrepareBoundStart(context.Background(), req); !errors.Is(err, ErrIdentity) {
					t.Fatal("old task silently moved to new version", err)
				}
			}
		})
	}
}

func TestCompatibleMetadataAndCriticalProtocolFailures(t *testing.T) {
	a := &Adapter{}
	for _, stream := range []string{
		strings.Replace(successStream, `"usage":{`, `"metadata":{"future":"private-canary"},"usage":{`, 1),
		strings.Replace(successStream, `"output_tokens":1`, `"output_tokens":1,"future_counter":{"ignored":"private-canary"}`, 1),
	} {
		d, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream), EOF: true})
		if err != nil {
			t.Fatal("additive metadata rejected", err)
		}
		r, err := a.Complete(execution.CompletionRequest{Events: persisted(t, d.Events)})
		if err != nil || r.Kind != execution.TerminalReviewReady {
			t.Fatal(r, err)
		}
		for _, event := range d.Events {
			if strings.Contains(string(event.NormalizedJSON), "private-canary") {
				t.Fatal("discarded metadata persisted")
			}
		}
	}
	for _, frame := range []string{
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
		`{"type":"turn.completed","usage":null}`,
		`{"type":"turn.completed","status":"failed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`,
		`{"type":"item.completed","item":{"id":"c","type":"command_execution","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"c","type":"command_execution","status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"c","type":"command_execution","status":"in_progress","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"c","type":"command_execution","status":"unknown","exit_code":0}}`,
		`{"type":"item.updated","item":{"id":"c","type":"command_execution","status":"unknown"}}`,
		`{"type":"item.started","item":{"id":"c","type":"command_execution","status":"completed","exit_code":0}}`,
		`{"type":"item.completed","item":{"id":"f","type":"file_change","status":"declined"}}`,
		`{"type":"item.completed","item":{"id":"c","type":"new_execution_tool"}}`,
		`{"type":"item.aborted","item":{"id":"c","type":"command_execution"}}`,
		`{"type":"turn.new_terminal"}`,
		`{"type":"error"}`,
	} {
		if _, err := a.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(frame + "\n"), EOF: true}); !errors.Is(err, execution.ErrEventStreamInvalid) {
			t.Fatal("critical protocol shape accepted", frame, err)
		}
	}
}
