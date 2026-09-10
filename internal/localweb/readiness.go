package localweb

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/preflight"
)

type readinessState string

const (
	readinessChecking readinessState = "checking"
	readinessReady    readinessState = "ready"
	readinessBlocked  readinessState = "blocked"
)

type readinessItem struct {
	Key      string         `json:"key"`
	State    readinessState `json:"state"`
	Observed string         `json:"observed"`
	Required string         `json:"required"`
	Action   string         `json:"action"`
}

type readinessView struct {
	CheckedAt        time.Time       `json:"checkedAt"`
	InputFingerprint string          `json:"inputFingerprint"`
	Items            []readinessItem `json:"items"`
}

type readinessInputs struct {
	Report                preflight.Report
	Pi                    piRuntimeStatus
	Verifier              verifierRuntimeStatus
	BaselineErr           error
	TaskWorktreesRequired bool
	TaskWorktreesReady    bool
	CheckedAt             time.Time
}

type readinessDescription struct {
	key      string
	checking string
	ready    string
	required string
	action   string
}

var readinessDescriptions = [...]readinessDescription{
	{key: "source_baseline", checking: "installed Source and Baseline check has not completed", ready: "installed manifest-bound Source Baseline v6 verified", required: "installed manifest-bound Chora Source Baseline v6", action: "Restore the marker-bound installation, then Refresh readiness."},
	{key: "pi_image", checking: "Pi Runtime, Sandbox, and pinned image check has not completed", ready: "Pi Runtime, Sandbox, and pinned images verified", required: "configured Pi Runtime with the pinned Agent and boundary images", action: "Restore the pinned Pi Runtime, Sandbox, and images; restart Chora; then Refresh readiness."},
	{key: "docker_engine", checking: "Docker engine and context check has not completed", ready: "pinned Docker engine and Colima context verified", required: "Docker client and server 29.6.1 using context colima", action: "Restore the pinned Docker engine and context; restart Chora; then Refresh readiness."},
	{key: "colima", checking: "Colima version and capacity check has not completed", ready: "pinned running Colima profile and capacity verified", required: "Colima 0.10.3 with the declared architecture and minimum capacity", action: "Restore the pinned Colima profile and capacity; restart Chora; then Refresh readiness."},
	{key: "oauth", checking: "OAuth provider entry check has not completed", ready: "owner-only openai-codex OAuth provider entry verified", required: "readable owner-only openai-codex OAuth provider entry", action: "Authenticate or Refresh the owner-only OAuth provider entry; restart Chora; then Refresh readiness."},
	{key: "ca_proxy_model", checking: "CA, proxy, and model reachability check has not completed", ready: "pinned CA, fixed proxy, and allowlisted model reachability verified", required: "pinned CA and authenticated allowlisted model reachability through the fixed proxy", action: "Restore the pinned CA, proxy, and model route; restart Chora; then Refresh readiness."},
	{key: "verifier", checking: "independent Verifier check has not completed", ready: "independent pinned Verifier image, policy, and Baseline verified", required: "configured independent Verifier with pinned image, policy, and Baseline", action: "Restore the independent Verifier dependencies; restart Chora; then Refresh readiness."},
	{key: "task_worktrees", checking: "Task worktree manager check has not completed", ready: "Task-owned Git worktree manager verified", required: "an exact supported Git repository whose parent can hold direct sibling worktrees", action: "Restart Chora with --repository; Chora places each Task worktree directly beside that repository, then Refresh readiness."},
	{key: "owned_residue", checking: "owned-resource residue check has not completed", ready: "no disallowed Chora-owned resource residue reported", required: "no disallowed Chora-owned containers, networks, or attempt Workspaces", action: "Run bounded cleanup for the reported Chora-owned residue, then Refresh readiness."},
}

func projectReadiness(inputs readinessInputs) readinessView {
	items := make([]readinessItem, len(readinessDescriptions))
	for index, description := range readinessDescriptions {
		items[index] = readinessItem{
			Key: description.key, State: readinessChecking, Observed: description.checking,
			Required: description.required, Action: "Refresh readiness.",
		}
	}
	view := readinessView{
		CheckedAt: inputs.CheckedAt, InputFingerprint: publicFingerprint(inputs.Report.InputFingerprint), Items: items,
	}

	if inputs.Report.Status == preflight.StatusPassed && !inputs.Report.ResourcesCreated {
		for index, description := range readinessDescriptions {
			view.Items[index].State = readinessReady
			view.Items[index].Observed = description.ready
			view.Items[index].Action = "No action required."
		}
	} else if inputs.Report.Status == preflight.StatusFailed && inputs.Report.Failure != nil {
		key := readinessKeyForBoundary(inputs.Report.Failure.Boundary)
		blockReadinessItem(&view, key, inputs.Report.Failure.Observed, inputs.Report.Failure.Required, inputs.Report.Failure.Action)
	} else if inputs.Report.Status == preflight.StatusPassed && inputs.Report.ResourcesCreated {
		blockReadinessItem(&view, "owned_residue", "preflight reported resource creation", "zero resources created during readiness checks", "Stop and clean all Chora-owned resources, then Refresh readiness.")
	} else if inputs.Report.Status == preflight.StatusFailed {
		blockReadinessItem(&view, "source_baseline", "preflight failed without an actionable boundary", "an actionable closed preflight report", "Restart Chora from the marker-bound installation, then Refresh readiness.")
	}

	if inputs.BaselineErr != nil {
		blockReadinessItem(&view, "source_baseline", "installed Source Baseline verification failed", readinessDescriptionFor("source_baseline").required, readinessDescriptionFor("source_baseline").action)
	} else {
		markStartupReady(&view, "source_baseline", "installed manifest-bound Source Baseline v6 verified")
	}
	if inputs.Pi.Enabled {
		markStartupReady(&view, "pi_image", "Pi Runtime, Sandbox, and pinned images configured")
	} else {
		blockReadinessItem(&view, "pi_image", "Pi Runtime or Sandbox startup dependency unavailable", readinessDescriptionFor("pi_image").required, readinessDescriptionFor("pi_image").action)
	}
	if inputs.Verifier.Enabled {
		markStartupReady(&view, "verifier", "independent pinned Verifier configured")
	} else {
		blockReadinessItem(&view, "verifier", "independent Verifier startup dependency unavailable", readinessDescriptionFor("verifier").required, readinessDescriptionFor("verifier").action)
	}
	if !inputs.TaskWorktreesRequired {
		markStartupReady(&view, "task_worktrees", "Task worktrees are not required in diagnostic mode")
	} else if inputs.TaskWorktreesReady {
		markStartupReady(&view, "task_worktrees", "Task-owned Git worktree manager verified")
	} else {
		blockReadinessItem(&view, "task_worktrees", "Task worktree manager is unavailable or does not match the installed repository authority", readinessDescriptionFor("task_worktrees").required, readinessDescriptionFor("task_worktrees").action)
	}
	return view
}

func markStartupReady(view *readinessView, key, observed string) {
	for index := range view.Items {
		if view.Items[index].Key == key && view.Items[index].State == readinessChecking {
			view.Items[index].State = readinessReady
			view.Items[index].Observed = observed
			view.Items[index].Action = "No action required."
			return
		}
	}
}

func blockReadinessItem(view *readinessView, key, observed, required, action string) {
	description := readinessDescriptionFor(key)
	for index := range view.Items {
		if view.Items[index].Key != key {
			continue
		}
		view.Items[index].State = readinessBlocked
		view.Items[index].Observed = publicReadinessText(observed, "readiness dependency unavailable")
		view.Items[index].Required = publicReadinessText(required, description.required)
		view.Items[index].Action = publicReadinessText(action, description.action)
		return
	}
}

func readinessDescriptionFor(key string) readinessDescription {
	for _, description := range readinessDescriptions {
		if description.key == key {
			return description
		}
	}
	return readinessDescriptions[0]
}

func readinessKeyForBoundary(boundary string) string {
	boundary = strings.ToLower(strings.TrimSpace(boundary))
	switch {
	case strings.HasPrefix(boundary, "image.agent"), strings.HasPrefix(boundary, "image.boundary"), strings.HasPrefix(boundary, "tool.pi"):
		return "pi_image"
	case strings.HasPrefix(boundary, "docker."):
		return "docker_engine"
	case strings.HasPrefix(boundary, "colima."):
		return "colima"
	case strings.HasPrefix(boundary, "oauth."):
		return "oauth"
	case strings.HasPrefix(boundary, "trust."), strings.HasPrefix(boundary, "proxy."), strings.HasPrefix(boundary, "model."):
		return "ca_proxy_model"
	case strings.HasPrefix(boundary, "image.verifier"), strings.HasPrefix(boundary, "verifier."):
		return "verifier"
	case strings.HasPrefix(boundary, "residue."):
		return "owned_residue"
	case boundary == "preflight.resource-order":
		return "owned_residue"
	case boundary == "preflight.unconfigured":
		return "pi_image"
	default:
		// Platform, toolchain, root, bundle, data identity, port, input drift,
		// and internal-integrity failures all prevent trusting the installed
		// Source/Baseline identity.
		return "source_baseline"
	}
}

var (
	authorizationPattern = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	secretFieldPattern   = regexp.MustCompile(`(?i)\b(access[ _-]?token|refresh[ _-]?token|id[ _-]?token|token|credential|secret|password|authorization|api[ _-]?key)(?:\s*(?::|=|\bwas\b|\bis\b)?\s*)["']?([^\s,;"']+)["']?`)
	openAITokenPattern   = regexp.MustCompile(`\b(?:sk|sess)-[A-Za-z0-9_-]{6,}\b`)
	jwtPattern           = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+){1,2}\b`)
	urlUserInfoPattern   = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
	opaqueTokenPattern   = regexp.MustCompile(`[A-Za-z0-9_+/=-]{24,}`)
	hexDigestPattern     = regexp.MustCompile(`(?i)^[0-9a-f]{64}$`)
)

func publicReadinessText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	value = authorizationPattern.ReplaceAllString(value, "authorization [REDACTED]")
	value = secretFieldPattern.ReplaceAllString(value, "$1 [REDACTED]")
	value = openAITokenPattern.ReplaceAllString(value, "[REDACTED]")
	value = jwtPattern.ReplaceAllString(value, "[REDACTED]")
	value = urlUserInfoPattern.ReplaceAllString(value, "$1[REDACTED]@")
	value = opaqueTokenPattern.ReplaceAllStringFunc(value, func(candidate string) string {
		if hexDigestPattern.MatchString(candidate) {
			return candidate
		}
		return "[REDACTED]"
	})
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		value = fallback
	}
	return truncateReadinessText(value, 512)
}

func truncateReadinessText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return strings.TrimSpace(value[:limit]) + "…"
}

func publicFingerprint(value string) string {
	value = strings.TrimSpace(value)
	if !hexDigestPattern.MatchString(value) {
		return ""
	}
	return strings.ToLower(value)
}
