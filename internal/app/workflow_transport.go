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

	"github.com/Yangyang96/chora/internal/domain"
)

// The transport identity binds the implemented serializer/admission/dispatch
// boundary. It is separate from the Pi runtime/catalog identity.
//
//go:embed workflow_transport.go workflow_dispatch.go workflow.go workflow_revision.go runs.go verified_retry.go
var workflowTransportSources embed.FS

func WorkflowTransportIdentity() string {
	var source bytes.Buffer
	for _, name := range []string{"workflow_transport.go", "workflow_dispatch.go", "workflow.go", "workflow_revision.go", "runs.go", "verified_retry.go"} {
		body, _ := workflowTransportSources.ReadFile(name)
		source.WriteString(name + ":" + strconv.Itoa(len(body)) + ":")
		source.Write(body)
	}
	source.WriteString(domain.WorkflowBudgetPolicyIdentity())
	return domain.WorkflowDigest(source.Bytes())
}

// WorkflowChatTransport is a deliberately narrow, non-streaming physical
// transport, not an SDK turn observer. It has no auth discovery, redirects,
// SDK retries, model fallback, reasoning or arbitrary upstream URL. Credentials
// must be passed explicitly by an authorized composition; they never enter the
// reservation, receipt, Pi process or error text.
// Native Pi is NOT composed with this transport yet and remains refused.
type WorkflowChatTransport struct {
	service   *Service
	key       string
	roundTrip http.RoundTripper
}

func NewWorkflowChatTransport(service *Service, explicitKey string, network http.RoundTripper) (*WorkflowChatTransport, error) {
	if service == nil || explicitKey == "" {
		return nil, ErrInvalidCommand
	}
	if network == nil {
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		// No reused connection or HTTP/2 retry machinery: one reservation can
		// cause only one non-idempotent POST, even on a transport failure.
		network = &http.Transport{Proxy: nil, Protocols: protocols, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, DisableKeepAlives: true}
	}
	// An injected connection is a trusted composition port (used for local HTTP
	// fixtures). Production uses the direct, retry-free POST transport above.
	return &WorkflowChatTransport{service: service, key: explicitKey, roundTrip: network}, nil
}

// workflowChatEnvelope uses the same pinned text-only, nonthinking byte-envelope
// premise as research v2, with a larger framing allowance for arbitrary Pi tool
// schemas. Unknown renderer branches are refused. This premise still requires
// independent renderer review; it is not a provider-side quota guarantee.
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
	bound := 8192 + 8*len(canonical) + 256*len(messages)
	if bound > 100000 {
		return 0, 0, ErrInvalidCommand
	}
	return bound, output, nil
}

func (transport *WorkflowChatTransport) Dispatch(ctx context.Context, workflowID string, runID domain.RunID, attemptID domain.AttemptID, requestID string, body []byte) (json.RawMessage, error) {
	// Send the same canonical object we admit, including duplicate-key
	// normalization; never validate one interpretation and dispatch another.
	var admitted any
	if len(body) > 256<<10 || json.Unmarshal(body, &admitted) != nil {
		return nil, ErrInvalidCommand
	}
	body, _ = json.Marshal(admitted)
	input, output, err := workflowChatEnvelope(body)
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
	reservation, err := transport.service.ReserveWorkflowDispatch(ctx, workflowID, runID, attemptID, requestID, WorkflowTransportIdentity(), domain.WorkflowDigest(body), input, output)
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
