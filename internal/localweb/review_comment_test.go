package localweb

import (
	"encoding/json"
	"testing"
)

func TestReviewCommentAcceptsCanonicalAndLegacyInput(t *testing.T) {
	for _, tc := range []struct {
		body, want string
		fail       bool
	}{
		{`{"comment":"修复边界条件"}`, "修复边界条件", false},
		{`{"note":"old client"}`, "old client", false},
		{`{"comment":"same","note":"same"}`, "same", false},
		{`{"comment":"","note":"old"}`, "", true},
		{`{"comment":"new","note":"old"}`, "", true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var input reviewCommentInput
			if err := json.Unmarshal([]byte(tc.body), &input); err != nil {
				t.Fatal(err)
			}
			got, err := input.value()
			if (err != nil) != tc.fail || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}
