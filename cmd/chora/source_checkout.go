package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/localweb"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/productinstall"
)

const sourceCheckoutLedgerID = "source-checkout-local-alpha-v1"

func runSourceCheckout(args []string, stdout, stderr io.Writer) int {
	return runSourceCheckoutWithListen(args, stdout, stderr, func(server *http.Server) error {
		return server.ListenAndServe()
	})
}

func runSourceCheckoutWithListen(args []string, stdout, stderr io.Writer, listen func(*http.Server) error) int {
	if listen == nil {
		return 2
	}
	flags := flag.NewFlagSet("source-checkout", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourceRoot := flags.String("source", "", "absolute Chora source checkout")
	repositoryRoot := flags.String("repository", "", "absolute Git repository used for the core Task")
	dataRoot := flags.String("data", "", "absolute owner-private Chora data root")
	authFile := flags.String("auth", "", "absolute owner-only Pi OAuth file")
	dockerCLI := flags.String("docker-cli", "", "absolute Docker CLI")
	dockerContext := flags.String("docker-context", "colima", "explicit Docker context")
	webRoot := flags.String("web", "", "built Web UI directory (default SOURCE/web/dist)")
	port := flags.Int("port", 8787, "loopback Chora port")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !canonicalAbsolute(*sourceRoot) || !canonicalAbsolute(*repositoryRoot) ||
		!canonicalAbsolute(*dataRoot) || !canonicalAbsolute(*authFile) || !canonicalAbsolute(*dockerCLI) ||
		!productIdentifier.MatchString(*dockerContext) || *port < 1 || *port > 65535 ||
		pathsOverlap(*dataRoot, *sourceRoot) || pathsOverlap(*dataRoot, *repositoryRoot) {
		fmt.Fprintln(stderr, "source-checkout requires absolute --source, --repository, --data, --auth, --docker-cli, an explicit --docker-context, a valid --port, and a data root outside source/repository")
		return 2
	}
	if *webRoot == "" {
		*webRoot = filepath.Join(*sourceRoot, "web", "dist")
	}
	if !canonicalAbsolute(*webRoot) || !pathInside(*sourceRoot, *webRoot) {
		fmt.Fprintln(stderr, "source-checkout Web build must be inside the source checkout")
		return 2
	}
	if err := requireSourceCheckoutAuth(*authFile); err != nil {
		fmt.Fprintln(stderr, "source-checkout OAuth authority is unavailable")
		return 1
	}
	if err := ensureSourceCheckoutDataRoot(*dataRoot); err != nil {
		fmt.Fprintln(stderr, "source-checkout data root is unavailable")
		return 1
	}

	target, err := sourceCheckoutDockerTarget(*dockerCLI, *dockerContext)
	if err != nil {
		fmt.Fprintln(stderr, "source-checkout Docker context authority is unavailable")
		return 1
	}
	runner, err := newExactDockerCLIRunner(target)
	if err != nil {
		fmt.Fprintln(stderr, "source-checkout exact Docker Runner is unavailable")
		return 1
	}
	ledger, err := dockersupervisor.OpenOperationLedger(runner, filepath.Join(*dataRoot, "runtime-authority"), sourceCheckoutLedgerID)
	if err != nil {
		fmt.Fprintln(stderr, "source-checkout Docker operation authority is unavailable")
		return 1
	}
	owned := localweb.NewDockerOperationLedgerOwnership(ledger)
	defer ledger.Close()

	ctx := context.Background()
	qualificationRunner := dockersupervisor.RunnerForOperationPhase(ledger, dockersupervisor.OperationPhaseSetup)
	identity, contract, qualification, err := qualifySourceCheckoutSandbox(ctx, qualificationRunner, target, filepath.Join(*dataRoot, "capability-probe"))
	if err != nil {
		fmt.Fprintf(stderr, "source-checkout Docker Sandbox qualification failed closed: %v\n", err)
		return 1
	}
	fingerprint := sourceCheckoutFingerprint(*sourceRoot, *repositoryRoot, *dataRoot, *authFile, target, identity, contract, qualification)
	options := localweb.ProductOptions{
		RepoRoot: *sourceRoot, RepositoryRoot: *repositoryRoot, PiAuthFile: *authFile,
		DockerRunner: ledger, DockerLedgerOwner: owned, DockerEngineIdentity: identity,
		DockerCapability: contract, DockerQualification: qualification,
		PiPreflight: func(context.Context) preflight.Report {
			return preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: fingerprint}
		},
		SourceCheckout: true,
	}
	roomServer, err := localweb.NewSourceCheckout(ctx, filepath.Join(*dataRoot, "chora.db"), *webRoot, log.New(stderr, "chora: ", log.LstdFlags), options)
	if err != nil {
		fmt.Fprintln(stderr, "source-checkout Chora startup failed closed")
		return 1
	}
	defer roomServer.Close()

	address := "127.0.0.1:" + fmt.Sprint(*port)
	httpServer := &http.Server{Addr: address, Handler: roomServer.Handler(), ReadHeaderTimeout: 5 * time.Second}
	fmt.Fprintf(stdout, "Chora Source-checkout Local Alpha listening on http://%s\n", address)
	err = listen(httpServer)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, "source-checkout Chora server stopped unexpectedly")
		return 1
	}
	return 0
}

func sourceCheckoutDockerTarget(cli, contextName string) (productinstall.EngineTarget, error) {
	configRoot := os.Getenv("DOCKER_CONFIG")
	if configRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return productinstall.EngineTarget{}, err
		}
		configRoot = filepath.Join(home, ".docker")
	}
	resolved, err := filepath.EvalSymlinks(configRoot)
	if err != nil || !canonicalAbsolute(resolved) {
		return productinstall.EngineTarget{}, errors.New("Docker configuration root is unavailable")
	}
	_, _, _, endpointDigest, err := exactDockerAuthorityIdentity(resolved, contextName)
	if err != nil {
		return productinstall.EngineTarget{}, err
	}
	return productinstall.EngineTarget{CLIPath: cli, ContextName: contextName, EndpointDigest: endpointDigest}, nil
}

func qualifySourceCheckoutSandbox(ctx context.Context, runner dockersupervisor.CommandRunner, target productinstall.EngineTarget, runtimeRoot string) (dockersupervisor.EngineIdentity, dockersupervisor.CapabilityProbeContract, dockersupervisor.EngineQualification, error) {
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		return dockersupervisor.EngineIdentity{}, dockersupervisor.CapabilityProbeContract{}, dockersupervisor.EngineQualification{}, fmt.Errorf("create capability Probe root: %w", err)
	}
	if err := os.Chmod(runtimeRoot, 0o700); err != nil {
		return dockersupervisor.EngineIdentity{}, dockersupervisor.CapabilityProbeContract{}, dockersupervisor.EngineQualification{}, fmt.Errorf("secure capability Probe root: %w", err)
	}
	identity, err := dockersupervisor.ObserveEngine(ctx, runner)
	if err != nil {
		return dockersupervisor.EngineIdentity{}, dockersupervisor.CapabilityProbeContract{}, dockersupervisor.EngineQualification{}, fmt.Errorf("observe explicit Docker Engine: %w", err)
	}
	if identity.ContextName() != target.ContextName || identity.ContextEndpointDigest() != target.EndpointDigest {
		return dockersupervisor.EngineIdentity{}, dockersupervisor.CapabilityProbeContract{}, dockersupervisor.EngineQualification{}, fmt.Errorf("Docker Engine identity does not match the explicit context: observed context %q, expected %q, endpoint match %t", identity.ContextName(), target.ContextName, identity.ContextEndpointDigest() == target.EndpointDigest)
	}
	contract, err := dockersupervisor.NewCapabilityProbeContract(agentpi.AttemptImageID, agentpi.PolicySHA256)
	if err != nil {
		return dockersupervisor.EngineIdentity{}, dockersupervisor.CapabilityProbeContract{}, dockersupervisor.EngineQualification{}, err
	}
	probe, err := dockersupervisor.NewCapabilityProbe(dockersupervisor.CapabilityProbeConfig{Runner: runner, RuntimeRoot: runtimeRoot, Identity: identity, Contract: contract})
	if err != nil {
		return dockersupervisor.EngineIdentity{}, dockersupervisor.CapabilityProbeContract{}, dockersupervisor.EngineQualification{}, err
	}
	qualification, err := probe.Probe(ctx, dockersupervisor.AuthorizeCapabilityProbe())
	if err := sourceCheckoutQualificationError(err, qualification.ValidFor(identity, contract)); err != nil {
		return dockersupervisor.EngineIdentity{}, dockersupervisor.CapabilityProbeContract{}, dockersupervisor.EngineQualification{}, err
	}
	return identity, contract, qualification, nil
}

func sourceCheckoutQualificationError(probeErr error, valid bool) error {
	if probeErr != nil {
		return fmt.Errorf("Docker Sandbox capability probe failed: %w", probeErr)
	}
	if !valid {
		return errors.New("Docker Sandbox capability probe returned an invalid qualification")
	}
	return nil
}

func requireSourceCheckoutAuth(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("OAuth file must be owner-only and regular")
	}
	return nil
}

func ensureSourceCheckoutDataRoot(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("data root must be an owner-private real directory")
	}
	return nil
}

func sourceCheckoutFingerprint(source, repository, data, auth string, target productinstall.EngineTarget, identity dockersupervisor.EngineIdentity, contract dockersupervisor.CapabilityProbeContract, qualification dockersupervisor.EngineQualification) string {
	digest := sha256.Sum256([]byte(source + "\x00" + repository + "\x00" + data + "\x00" + auth + "\x00" + target.CLIPath + "\x00" + target.ContextName + "\x00" + target.EndpointDigest + "\x00" + string(identity.CanonicalJSON()) + "\x00" + contract.Digest() + "\x00" + qualification.Digest()))
	return hex.EncodeToString(digest[:])
}
