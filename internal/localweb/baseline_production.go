//go:build !chora_e2e

package localweb

import "github.com/Yangyang96/chora/internal/verifier"

// Production retains the exact historical frozen baseline authority.
const productBaselineDigest = verifier.M1FrozenBaselineDigestHex
