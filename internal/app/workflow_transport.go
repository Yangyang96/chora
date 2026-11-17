package app

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/budgetchannel"
	"github.com/Yangyang96/chora/internal/domain"
)

// The transport identity binds the implemented serializer/admission/dispatch
// boundary. It is separate from the Pi runtime/catalog identity.
//
//go:embed workflow_transport.go workflow_dispatch.go workflow.go workflow_revision.go runs.go verified_retry.go workflow_restricted.go workflow_stop_observation.go
var workflowTransportSources embed.FS

func WorkflowTransportIdentity() string {
	var source bytes.Buffer
	for _, name := range []string{"workflow_transport.go", "workflow_dispatch.go", "workflow.go", "workflow_revision.go", "runs.go", "verified_retry.go", "workflow_restricted.go", "workflow_stop_observation.go"} {
		body, _ := workflowTransportSources.ReadFile(name)
		source.WriteString(name + ":" + strconv.Itoa(len(body)) + ":")
		source.Write(body)
	}
	source.WriteString(domain.WorkflowBudgetPolicyIdentity())
	source.WriteString(budgetchannel.ProtocolIdentity())
	return domain.WorkflowDigest(source.Bytes())
}

// WorkflowChatTransport is a deliberately narrow, non-streaming physical
// transport, not an SDK turn observer. It has no auth discovery, redirects,
// SDK retries, model fallback, reasoning or arbitrary upstream URL. Credentials
// must be passed explicitly by an authorized composition; they never enter the
// reservation, receipt, Pi process or error text.
// The restricted provider source remains unavailable until renderer/billing and
// independent runtime qualification are established.
type WorkflowChatTransport struct {
	service    *Service
	inputBound func([]byte) (int, int, error)
	key        string
	roundTrip  http.RoundTripper
}

// Production has no transport or proof injection port. The renderer and billing
// proofs have not been established, so Dispatch refuses before reserving/sending.
func NewWorkflowChatTransport(service *Service, explicitKey string) (*WorkflowChatTransport, error) {
	if service == nil || explicitKey == "" {
		return nil, ErrInvalidCommand
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	network := &http.Transport{Proxy: nil, Protocols: protocols, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableKeepAlives: true}
	return &WorkflowChatTransport{service: service, key: explicitKey, roundTrip: network}, nil
}

var ErrBoundUnproven = errors.New("bound_unproven: exact input renderer and billing contract required")

// workflowChatEnvelope is an UNPROVEN byte envelope for offline fixtures only.
// It is never installed as the production inputBound strategy. A production
// strategy must be bound to exact renderer and billing evidence; increasing
// constants, observed usage, or account balance cannot provide that proof.
func workflowChatEnvelope(raw []byte) (int, int, error) {
	if len(raw) > 256<<10 {
		return 0, 0, ErrInvalidCommand
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return 0, 0, ErrInvalidCommand
	}
	for key := range body {
		switch key {
		case "model", "messages", "tools", "tool_choice", "max_tokens", "thinking", "stream":
		default:
			return 0, 0, ErrInvalidCommand
		}
	}
	var model, choice string
	var output int
	var thinking map[string]string
	if json.Unmarshal(body["model"], &model) != nil || model != "deepseek-v4-pro" || json.Unmarshal(body["max_tokens"], &output) != nil || output < 1 || output > 2048 || json.Unmarshal(body["thinking"], &thinking) != nil || len(thinking) != 1 || thinking["type"] != "disabled" {
		return 0, 0, ErrInvalidCommand
	}
	if v, ok := body["stream"]; ok && string(v) != "false" {
		return 0, 0, ErrInvalidCommand
	}
	if v, ok := body["tool_choice"]; ok && (json.Unmarshal(v, &choice) != nil || (choice != "auto" && choice != "required")) {
		return 0, 0, ErrInvalidCommand
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil || len(messages) == 0 || len(messages) > 1000 {
		return 0, 0, ErrInvalidCommand
	}
	for _, message := range messages {
		for key := range message {
			switch key {
			case "role", "content", "tool_calls", "tool_call_id":
			default:
				return 0, 0, ErrInvalidCommand
			}
		}
		var role string
		if json.Unmarshal(message["role"], &role) != nil {
			return 0, 0, ErrInvalidCommand
		}
		switch role {
		case "system", "user", "assistant", "tool":
		default:
			return 0, 0, ErrInvalidCommand
		}
		var content *string
		if json.Unmarshal(message["content"], &content) != nil {
			return 0, 0, ErrInvalidCommand
		}
		if calls, ok := message["tool_calls"]; ok && !workflowFunctionCalls(calls, role) {
			return 0, 0, ErrInvalidCommand
		}
		if id, ok := message["tool_call_id"]; ok {
			var value string
			if role != "tool" || json.Unmarshal(id, &value) != nil || value == "" {
				return 0, 0, ErrInvalidCommand
			}
		}
	}
	if tools, ok := body["tools"]; ok && !workflowFunctionTools(tools) {
		return 0, 0, ErrInvalidCommand
	}
	// JSON with ASCII escaping expands every Unicode scalar to at least its
	// UTF-8 length. A large per-byte allowance also covers schema/call framing;
	// the per-message allowance covers empty constructs and provider framing.
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return 0, 0, ErrInvalidCommand
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return 0, 0, err
	}
	// Count the ASCII-escaped representation, including surrogate pairs,
	// without changing the canonical UTF-8 body actually sent to the provider.
	byteBound := len(canonical)
	for _, r := range string(canonical) {
		if r > 127 {
			size := 6
			if r > 0xffff {
				size = 12
			}
			byteBound += size - utf8.RuneLen(r)
		}
	}
	bound := 8192 + 8*byteBound + 256*len(messages)
	if bound > 100000 {
		return 0, 0, ErrInvalidCommand
	}
	return bound, output, nil
}

func workflowExactKeys(value map[string]json.RawMessage, allowed ...string) bool {
	for key := range value {
		found := false
		for _, candidate := range allowed {
			if key == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func workflowFunctionCalls(raw json.RawMessage, role string) bool {
	var calls []map[string]json.RawMessage
	if role != "assistant" || json.Unmarshal(raw, &calls) != nil || calls == nil || len(calls) > 16 {
		return false
	}
	for _, call := range calls {
		if !workflowExactKeys(call, "id", "type", "function") {
			return false
		}
		var id, kind string
		var function map[string]json.RawMessage
		if json.Unmarshal(call["id"], &id) != nil || id == "" || json.Unmarshal(call["type"], &kind) != nil || kind != "function" || json.Unmarshal(call["function"], &function) != nil || !workflowExactKeys(function, "name", "arguments") {
			return false
		}
		var name, arguments string
		var object map[string]json.RawMessage
		if json.Unmarshal(function["name"], &name) != nil || name == "" || json.Unmarshal(function["arguments"], &arguments) != nil || json.Unmarshal([]byte(arguments), &object) != nil || object == nil || len(object) > 128 {
			return false
		}
	}
	return true
}

func workflowFunctionTools(raw json.RawMessage) bool {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || tools == nil || len(tools) > 32 {
		return false
	}
	for _, tool := range tools {
		var kind string
		var function map[string]json.RawMessage
		if !workflowExactKeys(tool, "type", "function") || json.Unmarshal(tool["type"], &kind) != nil || kind != "function" || json.Unmarshal(tool["function"], &function) != nil || !workflowExactKeys(function, "name", "description", "parameters") {
			return false
		}
		var name, description string
		if json.Unmarshal(function["name"], &name) != nil || len(name) < 1 || len(name) > 64 {
			return false
		}
		if value, ok := function["description"]; ok && json.Unmarshal(value, &description) != nil {
			return false
		}
		if !workflowTextSchema(function["parameters"], 0) {
			return false
		}
	}
	return true
}

// Deliberately admit a small local-function schema surface. Provider-native
// web/file/image tools, remote references and unknown schema renderers are not
// text/function requests with this input-envelope premise.
func workflowTextSchema(raw json.RawMessage, depth int) bool {
	if depth > 16 {
		return false
	}
	var schema map[string]json.RawMessage
	var kind, description string
	if json.Unmarshal(raw, &schema) != nil || json.Unmarshal(schema["type"], &kind) != nil {
		return false
	}
	if value, ok := schema["description"]; ok && json.Unmarshal(value, &description) != nil {
		return false
	}
	switch kind {
	case "object":
		if !workflowExactKeys(schema, "type", "description", "properties", "required", "additionalProperties") {
			return false
		}
		var properties map[string]json.RawMessage
		var required []string
		var additional bool
		if value, ok := schema["properties"]; ok && (json.Unmarshal(value, &properties) != nil || len(properties) > 128) {
			return false
		}
		if value, ok := schema["required"]; ok && json.Unmarshal(value, &required) != nil {
			return false
		}
		if value, ok := schema["additionalProperties"]; ok && json.Unmarshal(value, &additional) != nil {
			return false
		}
		for _, value := range properties {
			if !workflowTextSchema(value, depth+1) {
				return false
			}
		}
		for _, name := range required {
			if _, ok := properties[name]; !ok {
				return false
			}
		}
	case "array":
		if !workflowExactKeys(schema, "type", "description", "items") || !workflowTextSchema(schema["items"], depth+1) {
			return false
		}
	case "string":
		if !workflowExactKeys(schema, "type", "description", "enum") {
			return false
		}
		if value, ok := schema["enum"]; ok {
			var choices []string
			if json.Unmarshal(value, &choices) != nil {
				return false
			}
		}
	case "number", "integer", "boolean", "null":
		if !workflowExactKeys(schema, "type", "description") {
			return false
		}
	default:
		return false
	}
	return true
}

func (transport *WorkflowChatTransport) Dispatch(ctx context.Context, workflowID string, runID domain.RunID, attemptID domain.AttemptID, requestID string, body []byte) (json.RawMessage, error) {
	return transport.dispatch(ctx, workflowID, runID, attemptID, requestID, body, nil)
}
func (transport *WorkflowChatTransport) dispatch(ctx context.Context, workflowID string, runID domain.RunID, attemptID domain.AttemptID, requestID string, body []byte, bound *domain.RestrictedLaunch) (json.RawMessage, error) {
	// Send the same canonical object we admit, including duplicate-key
	// normalization; never validate one interpretation and dispatch another.
	var admitted any
	if len(body) > 256<<10 || !budgetchannel.JSONDepth(body, 32) || json.Unmarshal(body, &admitted) != nil {
		return nil, ErrInvalidCommand
	}
	body, _ = json.Marshal(admitted)
	if transport.inputBound == nil {
		return nil, ErrBoundUnproven
	}
	input, output, err := transport.inputBound(body)
	if err != nil {
		return nil, err
	}
	w, err := transport.service.deps.Store.Reader().GetWorkflow(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if w.PiAuthorization == nil || w.PiAuthorization.Model.Record().Provider != "deepseek" || w.PiAuthorization.Model.Record().ModelID != "deepseek-v4-pro" {
		return nil, ErrUnauthorizedCommand
	}
	if bound != nil {
		if err = transport.service.restrictedReplay(ctx, *bound, requestID, domain.WorkflowDigest(body)); err != nil {
			return nil, err
		}
	}
	reservation, err := transport.service.reserveWorkflowDispatch(ctx, workflowID, runID, attemptID, requestID, WorkflowTransportIdentity(), domain.WorkflowDigest(body), input, output, bound)
	if err != nil {
		return nil, err
	}
	finish := func(prompt, completion int, known bool) error {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return transport.service.FinishWorkflowDispatch(cleanup, workflowID, reservation, prompt, completion, known)
	}
	dispatchCtx, cancel := context.WithDeadline(ctx, reservation.Deadline)
	defer cancel()
	if bound != nil {
		current, e := transport.service.deps.Store.Reader().GetWorkflow(dispatchCtx, workflowID)
		if e == nil {
			e = restrictedLaunchAuthority(dispatchCtx, transport.service.deps.Store.Reader(), current, *bound)
		}
		if e != nil {
			_ = finish(0, 0, false)
			return nil, e
		}
		done := make(chan struct{})
		go func() { defer close(done); transport.service.watchRestrictedDispatch(dispatchCtx, cancel, *bound) }()
		defer func() { cancel(); <-done }()
	}
	request, err := http.NewRequestWithContext(dispatchCtx, http.MethodPost, "https://api.deepseek.com/chat/completions", bytes.NewReader(body))
	if err != nil {
		_ = finish(0, 0, false)
		return nil, ErrInvalidCommand
	}
	request.Header.Set("Authorization", "Bearer "+transport.key)
	request.Header.Set("Content-Type", "application/json")
	response, err := transport.roundTrip.RoundTrip(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		_ = finish(0, 0, false)
		return nil, errors.New("provider dispatch outcome unknown")
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	var receipt struct {
		Model string `json:"model"`
		Usage *struct {
			Prompt     *int `json:"prompt_tokens"`
			Completion *int `json:"completion_tokens"`
			Total      *int `json:"total_tokens"`
		} `json:"usage"`
	}
	known := readErr == nil && len(raw) <= 2<<20 && response.StatusCode == 200 && json.Unmarshal(raw, &receipt) == nil && receipt.Model == "deepseek-v4-pro" && receipt.Usage != nil && receipt.Usage.Prompt != nil && receipt.Usage.Completion != nil && receipt.Usage.Total != nil
	prompt, completion := 0, 0
	if known {
		prompt, completion = *receipt.Usage.Prompt, *receipt.Usage.Completion
		known = prompt >= 0 && completion >= 0 && prompt <= input && completion <= output && *receipt.Usage.Total == prompt+completion && time.Now().Before(reservation.Deadline)
	}
	if err = finish(prompt, completion, known); err != nil {
		return nil, errors.New("provider result could not be durably reconciled")
	}
	if !known {
		return nil, errors.New("provider result unknown or outside reserved limits (status " + strconv.Itoa(response.StatusCode) + ")")
	}
	return raw, nil
}
