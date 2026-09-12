package localweb

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/pidistribution"
	"github.com/Yangyang96/chora/internal/releaseassets"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

type piComposition struct {
	isolatedSource    agentpi.IsolatedSource
	adapter           execution.AgentAdapter
	dockerSupervisor  *dockersupervisor.Supervisor
	trustedSupervisor *trustedhost.Supervisor
	localSnapshot     pidistribution.LocalPiSnapshot
	attemptImageID    string
	boundaryImageID   string
	verifierImageID   string
}

func composePiRuntime(ctx context.Context, runtimeRoot, artifactRoot, repoRoot string, runtimeConfig, policy []byte, options ProductOptions, product bool, timeoutPolicy acceptanceTimeoutPolicy) (piComposition, error) {
	if product && options.SourceCheckout {
		if err := validateSourceCheckoutDockerAuthority(options); err != nil {
			return piComposition{}, fmt.Errorf("activate SourceCheckout Docker authority: %w", err)
		}
	}
	config := agentpi.Config{RuntimeConfig: runtimeConfig, Policy: policy}
	var failures []error
	var compositionSnapshot pidistribution.LocalPiSnapshot
	var verifierImageID string

	if product && !options.SourceCheckout {
		if options.ActivatedRelease != nil {
			release := *options.ActivatedRelease
			managed, managedErr := release.ImageForRole(releaseassets.RoleManagedPiRuntime)
			boundary, boundaryErr := release.ImageForRole(releaseassets.RoleNetworkBoundary)
			probe, probeErr := release.ImageForRole(releaseassets.RoleCapabilityProbe)
			independentVerifier, verifierErr := release.ImageForRole(releaseassets.RoleIndependentVerifier)
			if err := errors.Join(managedErr, boundaryErr, probeErr, verifierErr); err != nil {
				failures = append(failures, fmt.Errorf("resolve activated Docker roles: %w", err))
			} else if !exactActivatedArtifactAlias(probe, managed) {
				failures = append(failures, errors.New("M1 capability Probe role must explicitly alias the managed Pi Runtime image"))
			} else {
				source, err := agentpi.NewManagedImageSource(agentpi.ManagedImageSourceParams{
					RuntimeVersion: managed.RuntimePiVersion, Executable: agentpi.Executable,
					RuntimeConfigSHA256: agentpi.RuntimeConfigSHA256, RuntimeImageID: managed.LocalDockerConfigImageID,
					ReleaseProvenance: agentpi.ManagedReleaseProvenance{
						Kind:      agentpi.ManagedReleaseProvenanceActivatedRelease,
						ReleaseID: managed.ReleaseID, SpecSHA256: managed.SpecSHA256, ArtifactID: managed.ArtifactID,
						ArchiveFormat: managed.ArchiveFormat, ArchiveSHA256: managed.ArchiveSHA256,
						ArchiveSize: managed.ArchiveSize, DockerConfigImageID: managed.LocalDockerConfigImageID,
						PolicyID:   managed.PolicyID,
						PolicyPath: managed.PolicyPath, PolicySHA256: managed.PolicySHA256,
						PlatformOS: managed.Platform.OS, PlatformArchitecture: managed.Platform.Architecture,
						RuntimePiName: managed.RuntimePiName, RuntimePiVersion: managed.RuntimePiVersion,
						RuntimePiNPMIntegrity: managed.RuntimePiNPMIntegrity, RuntimeNodeVersion: managed.RuntimeNodeVersion,
						RuntimeNodeExecutable: managed.RuntimeNodeExecutable, RuntimeNodeVersionOutput: managed.RuntimeNodeVersionOutput,
						EntrypointJSON: managed.EntrypointJSON,
					},
				})
				if err != nil {
					failures = append(failures, fmt.Errorf("bind activated managed Pi source: %w", err))
				} else {
					config.ManagedImageSource = source
					config.AttemptImageID = managed.LocalDockerConfigImageID
					config.BoundaryImageID = boundary.LocalDockerConfigImageID
					verifierImageID = independentVerifier.LocalDockerConfigImageID
				}
			}
		}
		if options.LocalPiSelection.Configured() || options.LocalPiResolver != nil {
			if options.LocalPiResolver == nil || !options.LocalPiSelection.Configured() {
				failures = append(failures, errors.New("local Pi activation requires both one Resolver and its immutable Selection"))
			} else {
				snapshot, err := options.LocalPiResolver.Snapshot(ctx, filepath.Join(runtimeRoot, "pi-runtime-snapshots"), options.LocalPiSelection)
				if err != nil {
					failures = append(failures, fmt.Errorf("snapshot activated local Pi source: %w", err))
				} else {
					local, localErr := localPiSourceFromSelection(snapshot.Selection())
					if localErr != nil {
						failures = append(failures, fmt.Errorf("bind activated local Pi snapshot: %w", localErr))
					} else {
						config.LocalPiSource = local
						config.ValidateLocalSource = localPiSnapshotValidator(snapshot)
						compositionSnapshot = snapshot
					}
				}
			}
		}
	} else {
		// Development and SourceCheckout must share one immutable managed-source
		// value across the Adapter and Docker Supervisor. Supplying only the image
		// lets the Adapter synthesize a source inside agentpi.New while leaving the
		// outer config's source identity zero, so the composed invocation is
		// correctly rejected as identity drift at child start.
		config.ManagedImageSource = agentpi.DefaultManagedImageSource()
		config.AttemptImageID = config.ManagedImageSource.RuntimeImageID()
		config.BoundaryImageID = agentpi.BoundaryImageID
	}

	adapter, err := agentpi.New(config)
	if err != nil {
		return piComposition{}, errors.Join(append(failures, err)...)
	}
	composition := piComposition{adapter: adapter, localSnapshot: compositionSnapshot, verifierImageID: verifierImageID}

	if config.ManagedImageSource.Configured() || config.AttemptImageID != "" {
		var dockerSupervisor *dockersupervisor.Supervisor
		var dockerErr error
		if product && options.DockerRunner == nil {
			dockerErr = errors.New("activated Docker execution requires the setup-qualified Runner")
		} else {
			dockerSupervisor, dockerErr = dockersupervisor.New(dockersupervisor.Config{
				Runner:      options.DockerRunner,
				RuntimeRoot: filepath.Join(runtimeRoot, "pi"), ArtifactRoot: artifactRoot,
				PolicyDigest: agentpi.PolicySHA256, AttemptImageID: config.AttemptImageID, BoundaryImageID: config.BoundaryImageID,
				VerifierImageID:       composition.verifierImageID,
				RuntimeSourceIdentity: fmt.Sprintf("%x", config.ManagedImageSource.SourceIdentity()),
				CredentialSource:      productCredentialSource(product, options.PiAuthFile),
				TrustAnchorSource:     filepath.Join(repoRoot, "contracts", "g2-m1a", "starpoint-root-ca-2048-g2.pem"),
				AttemptTimeout:        timeoutPolicy.AttemptTimeout, AcceptanceAuthority: timeoutPolicy.Controller,
				CapabilityContract: options.DockerCapability, EngineQualification: options.DockerQualification,
			})
		}
		if dockerErr == nil {
			_, dockerErr = dockerSupervisor.Verify(ctx)
		}
		if dockerErr == nil {
			dockerErr = dockerSupervisor.Recover(ctx)
		}
		if dockerErr != nil {
			failures = append(failures, fmt.Errorf("activate managed Pi Docker execution: %w", dockerErr))
		} else {
			composition.dockerSupervisor = dockerSupervisor
			composition.attemptImageID = config.AttemptImageID
			composition.boundaryImageID = config.BoundaryImageID
		}
	}

	if config.LocalPiSource.Configured() {
		selection := composition.localSnapshot.Selection()
		runtimeClosureIdentity := composition.localSnapshot.ContentSHA256()
		trustedSupervisor, trustedErr := trustedhost.New(trustedhost.Config{
			RuntimeRoot: filepath.Join(runtimeRoot, "pi-trusted"), AllowedAdapterID: agentpi.AdapterID,
			AllowedExecutable: trustedhost.ExecutableIdentity{
				Path: selection.Path(), ResolvedPath: selection.ResolvedPath(), SHA256: selection.ExecutableSHA256(),
			},
			AllowedArguments: agentpi.NativeRPCArguments(), MaxRuntime: timeoutPolicy.AttemptTimeout,
			RuntimeClosureIdentity: runtimeClosureIdentity,
			ValidateRuntimeClosure: func(context.Context) ([32]byte, error) {
				return composition.localSnapshot.Revalidate()
			},
		})
		if trustedErr != nil {
			failures = append(failures, fmt.Errorf("activate Trusted Local Pi execution: %w", trustedErr))
		} else {
			composition.trustedSupervisor = trustedSupervisor
		}
	}

	if composition.dockerSupervisor == nil && composition.trustedSupervisor == nil {
		return piComposition{}, errors.Join(failures...)
	}
	if composition.dockerSupervisor != nil && options.DockerCapability.ProbeImageID() != composition.attemptImageID {
		return piComposition{}, errors.New("activated capability contract does not bind the managed Pi image")
	}
	if composition.dockerSupervisor != nil && options.DockerCapability.SandboxPolicyDigest() != agentpi.PolicySHA256 {
		return piComposition{}, errors.New("activated capability contract does not bind the Pi Sandbox policy")
	}
	return composition, nil
}

func validatePiCompositionFingerprints(ctx context.Context, composition piComposition) error {
	fingerprinter, ok := composition.adapter.(execution.BindingFingerprinter)
	if !ok {
		return errors.New("Pi adapter does not support exact profile-bound fingerprinting")
	}
	var profiles []domain.AgentExecutionProfile
	if composition.dockerSupervisor != nil {
		if composition.isolatedSource.Configured() {
			profiles = append(profiles, domain.AgentExecutionProfileIsolatedLocal)
		} else {
			profiles = append(profiles, domain.AgentExecutionProfileMinimal, domain.AgentExecutionProfileStandard)
		}
	}
	if composition.trustedSupervisor != nil {
		profiles = append(profiles, domain.AgentExecutionProfileTrustedLocal)
	}
	if len(profiles) == 0 {
		return errors.New("Pi composition has no installed execution binding")
	}
	for _, profile := range profiles {
		binding, err := domain.NewAgentExecutionProfileBinding(profile)
		if err != nil {
			return err
		}
		fingerprint, err := fingerprinter.FingerprintForBinding(ctx, binding)
		if err != nil {
			return fmt.Errorf("fingerprint installed Pi profile %s: %w", profile, err)
		}
		if !fingerprint.Valid() {
			return fmt.Errorf("fingerprint installed Pi profile %s: invalid Runtime identity", profile)
		}
	}
	return nil
}

func piCompositionStatus(composition piComposition) piRuntimeStatus {
	status := piRuntimeStatus{Enabled: true, Reason: "profile-bound Pi Runtime execution activated", HostReadIsolation: "profile-bound", PolicyFingerprint: agentpi.PolicySHA256}
	switch {
	case composition.dockerSupervisor != nil && composition.trustedSupervisor != nil:
		status.Provider = "profile-bound"
		status.Image = composition.attemptImageID
	case composition.dockerSupervisor != nil:
		status.Provider = domain.DockerExecutionProvider
		status.Image = composition.attemptImageID
	case composition.trustedSupervisor != nil:
		status.Provider = domain.TrustedHostExecutionProvider
		status.HostReadIsolation = "not claimed"
	}
	return status
}

func sourceCheckoutPiRuntimeStatus(status piRuntimeStatus, options ProductOptions) (piRuntimeStatus, error) {
	if err := validateSourceCheckoutDockerAuthority(options); err != nil {
		return piRuntimeStatus{}, err
	}
	identity := options.DockerEngineIdentity
	status.EngineIdentityDigest = identity.Digest()
	status.DockerContext = identity.ContextName()
	status.ContextEndpointDigest = identity.ContextEndpointDigest()
	status.DockerServerVersion = identity.EngineVersion()
	// Preserve the established status field while making the server-version
	// meaning explicit for SourceCheckout callers.
	status.DockerVersion = identity.EngineVersion()
	return status, nil
}
