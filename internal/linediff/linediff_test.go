package linediff

import (
	"fmt"
	"testing"
)

func TestShortestEditsBoundsLargeDisjointComparison(t *testing.T) {
	const lineCount = 5000
	before := make([]string, lineCount)
	after := make([]string, lineCount)
	for index := 0; index < lineCount; index++ {
		before[index] = fmt.Sprintf("before-%04d", index)
		after[index] = fmt.Sprintf("after-%04d", index)
	}

	edits := ShortestEdits(before, after)
	if len(edits) != lineCount*2 {
		t.Fatalf("edit count = %d", len(edits))
	}
	for index, edit := range edits {
		if index < lineCount && edit.Kind != Delete || index >= lineCount && edit.Kind != Insert {
			t.Fatalf("edit %d = %#v", index, edit)
		}
	}
}

func TestShortestEditsKeepsLargeSparseChangesMinimal(t *testing.T) {
	const lineCount = 5000
	before := make([]string, lineCount)
	after := make([]string, lineCount)
	for index := 0; index < lineCount; index++ {
		before[index] = fmt.Sprintf("line-%04d", index)
		after[index] = before[index]
	}
	after[100] = "changed-near-start"
	after[4900] = "changed-near-end"

	edits := ShortestEdits(before, after)
	added, deleted := 0, 0
	for _, edit := range edits {
		if edit.Kind == Insert {
			added++
		}
		if edit.Kind == Delete {
			deleted++
		}
	}
	ranges := ContextRanges(edits, 3)
	if added != 2 || deleted != 2 || len(ranges) != 2 {
		t.Fatalf("sparse edits: added=%d deleted=%d ranges=%#v", added, deleted, ranges)
	}
}

func TestShortestEditsKeepsRepeatedLineSparseChangesMinimal(t *testing.T) {
	const lineCount = 5000
	before := make([]string, lineCount)
	after := make([]string, lineCount)
	for index := range before {
		before[index] = "same repeated line"
		after[index] = before[index]
	}
	after[100] = "changed-near-start"
	after[4900] = "changed-near-end"

	edits := ShortestEdits(before, after)
	added, deleted := 0, 0
	for _, edit := range edits {
		if edit.Kind == Insert {
			added++
		}
		if edit.Kind == Delete {
			deleted++
		}
	}
	ranges := ContextRanges(edits, 3)
	if added != 2 || deleted != 2 || len(ranges) != 2 {
		t.Fatalf("repeated sparse edits: added=%d deleted=%d ranges=%#v", added, deleted, ranges)
	}
}
