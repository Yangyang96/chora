package opencoderun

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

// Mutate only the chosen object, leaving its opaque siblings untouched.
func replaceEventField(t *testing.T, raw string, path []string, field, replacement string) string {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		t.Fatal(err)
	}
	if len(path) != 0 {
		object[path[0]] = json.RawMessage(replaceEventField(t, string(object[path[0]]), path[1:], field, replacement))
		encoded, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	needle := `"` + field + `":` + string(object[field])
	if !strings.Contains(raw, needle) {
		t.Fatal("mutation did not apply", field, raw)
	}
	return strings.Replace(raw, needle, replacement, 1)
}

func TestCriticalFieldAliasesAndDuplicates(t *testing.T) {
	frames := []struct {
		name, raw string
		fields    eventFields
	}{
		{"finish", strings.TrimSpace(resultStream), frameFields},
		{"text", strings.TrimSpace(messageStream), frameFields},
		{"tool", strings.TrimSpace(toolStream), frameFields},
		{"error", strings.TrimSpace(errorStream), frameFields},
	}
	for _, frame := range frames {
		var visit func(string, []string, eventFields)
		visit = func(raw string, path []string, schema eventFields) {
			var object map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &object); err != nil {
				t.Fatal(err)
			}
			for field, child := range schema {
				value, exists := object[field]
				if !exists {
					continue
				}
				aliases := []string{field, strings.ToUpper(field), fmt.Sprintf(`\u%04x%s`, field[0], field[1:])}
				if strings.Contains(field, "s") {
					aliases = append(aliases, strings.ReplaceAll(field, "s", "ſ"))
				}
				for i, alias := range aliases {
					// Single aliases preserve encoding/json compatibility.
					single := `"` + alias + `":` + string(value)
					mutated := replaceEventField(t, frame.raw, path, field, single)
					if _, err := (&Adapter{}).DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(mutated + "\n"), EOF: true}); err != nil {
						t.Fatalf("single alias %s/%v/%s: %v", frame.name, path, alias, err)
					}
					for order := 0; order < 2; order++ {
						first, second := `"`+field+`":`+string(value), single
						if order == 1 {
							first, second = second, first
						}
						mutated := replaceEventField(t, frame.raw, path, field, first+","+second)
						t.Run(fmt.Sprintf("%s/%s/%s/alias%d/order%d", frame.name, strings.Join(path, "/"), field, i, order), func(t *testing.T) {
							if _, err := (&Adapter{}).DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(mutated + "\n"), EOF: true}); err == nil {
								t.Fatal("ambiguous critical field accepted", mutated)
							}
						})
					}
				}
				if child != nil {
					visit(string(value), append(append([]string{}, path...), field), child)
				}
			}
		}
		visit(frame.raw, nil, frame.fields)
	}
}

func TestTerminalAuthorityAliasesAndOpaqueMetadata(t *testing.T) {
	for _, scope := range []string{"frame", "part", "Part"} {
		for _, field := range []string{"status", "outcome", "error", "is_error", "finish", "stop_reason"} {
			for _, key := range []string{field, strings.ToUpper(field), fmt.Sprintf(`\u%04x%s`, field[0], field[1:]), strings.ReplaceAll(field, "s", "ſ")} {
				stream := resultStream
				if scope != "frame" {
					stream = strings.Replace(stream, `"part":{`, `"`+scope+`":{"`+key+`":"failed",`, 1)
				} else {
					stream = strings.Replace(stream, `{`, `{"`+key+`":"failed",`, 1)
				}
				if _, err := (&Adapter{}).DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream), EOF: true}); err == nil {
					t.Fatal("terminal authority alias accepted", scope, key)
				}
			}
		}
	}
	// Keys in metadata and native tool payloads never enter the projection.
	for _, stream := range []string{
		strings.Replace(successStream, `"tokens":{`, `"metadata":{"reason":"error","Reason":"stop","reason":"stop","Status":"failed"},"tokens":{`, 1),
		strings.Replace(toolStream, `"input":{"path":"private-input"}`, `"input":{"status":"error","Status":"completed","type":"private-input"}`, 1),
	} {
		decoded, err := (&Adapter{}).DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: []byte(stream), EOF: true})
		if err != nil {
			t.Fatal("opaque extension rejected", err)
		}
		raw, _ := json.Marshal(decoded)
		if strings.Contains(string(raw), "private-input") || strings.Contains(string(raw), "Status") {
			t.Fatal("opaque extension leaked")
		}
	}
}
