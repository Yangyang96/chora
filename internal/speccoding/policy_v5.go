package speccoding

const PiRuntimeConfigSchemaVersionV5 = "chora.pi-runtime-config.v5"

// PiRuntimeConfigV5 changes only the pinned Pi package identity. All model,
// credential, trust, context, event, and result boundaries remain identical to
// the accepted v4 configuration.
type PiRuntimeConfigV5 = PiRuntimeConfigV4

func DecodePiRuntimeConfigV5(data []byte) (PiRuntimeConfigV5, error) {
	var config PiRuntimeConfigV5
	if err := decodeCandidatePolicy(data, &config); err != nil {
		return PiRuntimeConfigV5{}, err
	}
	if err := validatePiRuntimeConfigV5(config); err != nil {
		return PiRuntimeConfigV5{}, err
	}
	return config, nil
}

func validatePiRuntimeConfigV5(config PiRuntimeConfigV5) error {
	if config.SchemaVersion != PiRuntimeConfigSchemaVersionV5 ||
		config.Runtime.Package != "@earendil-works/pi-coding-agent" ||
		config.Runtime.Version != "0.84.2" ||
		config.Runtime.NPMIntegrity != piRuntimeNPMIntegrityV5 ||
		!slicesEqual(config.Runtime.Transport, append([]string{"pi"}, requiredPiArgumentsV3...)) {
		return invalidCandidatePolicy("v5 runtime identity or transport")
	}

	// Reuse the mature v4 boundary validator after normalizing only the two
	// versioned identity fields. This prevents a Runtime patch upgrade from
	// silently changing any security or authority boundary.
	legacy := PiRuntimeConfigV4(config)
	legacy.SchemaVersion = PiRuntimeConfigSchemaVersionV4
	legacy.Runtime.Version = "0.84.1"
	legacy.Runtime.NPMIntegrity = piRuntimeNPMIntegrity
	return validatePiRuntimeConfigV4(legacy)
}
