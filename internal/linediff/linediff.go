// Package linediff computes deterministic shortest edit scripts over complete
// text lines. It contains no Patch authority or filesystem behavior; callers
// decide whether the result is used to serialize or only to display a change.
package linediff

import "sort"

type Kind byte

const (
	Equal Kind = iota
	Delete
	Insert
)

type Edit struct {
	Kind Kind
	Line string
}

type Range struct {
	Start int
	End   int
}

// Exact comparison is deliberately bounded. Common prefixes, suffixes, and
// patience anchors split large sparse edits before an unmatched segment can
// fall back to a truthful full replacement.
const maxComparisonCells int64 = 4_000_000
const maxBoundedEditDistance = 512
const maxSparsePositionChanges = 512

type match struct {
	before int
	after  int
}

// ShortestEdits uses patience anchors plus a bounded Hirschberg LCS projection
// with linear working memory. It remains deterministic while avoiding retained
// trace matrices and keeping sparse changes in large files contextual.
func ShortestEdits(before, after []string) []Edit {
	edits := make([]Edit, 0, len(before)+len(after))
	appendBoundedEdits(&edits, before, after, 0)
	return edits
}

func appendBoundedEdits(edits *[]Edit, before, after []string, depth int) {
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix && before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	for _, line := range before[:prefix] {
		*edits = append(*edits, Edit{Kind: Equal, Line: line})
	}
	middleBefore := before[prefix : len(before)-suffix]
	middleAfter := after[prefix : len(after)-suffix]
	switch {
	case len(middleBefore) == 0:
		appendInsertions(edits, middleAfter)
	case len(middleAfter) == 0:
		appendDeletions(edits, middleBefore)
	default:
		if positional, ok := sparsePositionalEdits(middleBefore, middleAfter); ok {
			*edits = append(*edits, positional...)
		} else if bounded, ok := boundedMyersEdits(middleBefore, middleAfter, maxBoundedEditDistance); ok {
			*edits = append(*edits, bounded...)
		} else if int64(len(middleBefore))*int64(len(middleAfter)) <= maxComparisonCells {
			appendExactEdits(edits, middleBefore, middleAfter)
		} else {
			anchors := patienceAnchors(middleBefore, middleAfter)
			if len(anchors) == 0 || depth >= 64 {
				appendDeletions(edits, middleBefore)
				appendInsertions(edits, middleAfter)
			} else {
				beforeIndex, afterIndex := 0, 0
				for _, anchor := range anchors {
					appendBoundedEdits(edits, middleBefore[beforeIndex:anchor.before], middleAfter[afterIndex:anchor.after], depth+1)
					*edits = append(*edits, Edit{Kind: Equal, Line: middleBefore[anchor.before]})
					beforeIndex, afterIndex = anchor.before+1, anchor.after+1
				}
				appendBoundedEdits(edits, middleBefore[beforeIndex:], middleAfter[afterIndex:], depth+1)
			}
		}
	}
	for _, line := range before[len(before)-suffix:] {
		*edits = append(*edits, Edit{Kind: Equal, Line: line})
	}
}

// sparsePositionalEdits is an exact bounded fast path for large same-length
// files with a small number of replacements. Unlike content-only anchors, it
// remains informative when thousands of surrounding lines are identical.
func sparsePositionalEdits(before, after []string) ([]Edit, bool) {
	if len(before) != len(after) {
		return nil, false
	}
	changes := 0
	for index := range before {
		if before[index] != after[index] {
			changes++
			if changes > maxSparsePositionChanges {
				return nil, false
			}
		}
	}
	if changes == 0 {
		return nil, false
	}
	edits := make([]Edit, 0, len(before)+changes)
	for index, line := range before {
		if line == after[index] {
			edits = append(edits, Edit{Kind: Equal, Line: line})
			continue
		}
		edits = append(edits, Edit{Kind: Delete, Line: line}, Edit{Kind: Insert, Line: after[index]})
	}
	return edits, true
}

// boundedMyersEdits keeps both time and retained trace memory proportional to
// a fixed maximum edit distance, not to the total file length. It recovers
// minimal scripts for sparse insert/delete changes even when lines repeat.
func boundedMyersEdits(before, after []string, maximumDistance int) ([]Edit, bool) {
	limit := min(maximumDistance, len(before)+len(after))
	offset := limit + 1
	vector := make([]int, 2*limit+3)
	vector[offset+1] = 0
	trace := make([][]int, 0, limit+1)
	for distance := 0; distance <= limit; distance++ {
		trace = append(trace, append([]int(nil), vector...))
		for diagonal := -distance; diagonal <= distance; diagonal += 2 {
			index := offset + diagonal
			var x int
			if diagonal == -distance || diagonal != distance && vector[index-1] < vector[index+1] {
				x = vector[index+1]
			} else {
				x = vector[index-1] + 1
			}
			y := x - diagonal
			for x < len(before) && y >= 0 && y < len(after) && before[x] == after[y] {
				x++
				y++
			}
			vector[index] = x
			if x >= len(before) && y >= len(after) {
				return backtrackMyers(trace, before, after, offset), true
			}
		}
	}
	return nil, false
}

func backtrackMyers(trace [][]int, before, after []string, offset int) []Edit {
	x, y := len(before), len(after)
	reversed := make([]Edit, 0, x+y)
	for distance := len(trace) - 1; distance >= 0; distance-- {
		vector := trace[distance]
		diagonal := x - y
		index := offset + diagonal
		previousDiagonal := diagonal - 1
		if diagonal == -distance || diagonal != distance && vector[index-1] < vector[index+1] {
			previousDiagonal = diagonal + 1
		}
		previousX := vector[offset+previousDiagonal]
		previousY := previousX - previousDiagonal
		for x > previousX && y > previousY {
			x--
			y--
			reversed = append(reversed, Edit{Kind: Equal, Line: before[x]})
		}
		if distance == 0 {
			break
		}
		if x == previousX {
			y--
			reversed = append(reversed, Edit{Kind: Insert, Line: after[y]})
		} else {
			x--
			reversed = append(reversed, Edit{Kind: Delete, Line: before[x]})
		}
	}
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func appendExactEdits(edits *[]Edit, before, after []string) {
	matches := make([]match, 0, min(len(before), len(after)))
	collectMatches(before, after, 0, 0, &matches)
	beforeIndex, afterIndex := 0, 0
	for _, item := range matches {
		appendDeletions(edits, before[beforeIndex:item.before])
		appendInsertions(edits, after[afterIndex:item.after])
		*edits = append(*edits, Edit{Kind: Equal, Line: before[item.before]})
		beforeIndex, afterIndex = item.before+1, item.after+1
	}
	appendDeletions(edits, before[beforeIndex:])
	appendInsertions(edits, after[afterIndex:])
}

func appendDeletions(edits *[]Edit, lines []string) {
	for _, line := range lines {
		*edits = append(*edits, Edit{Kind: Delete, Line: line})
	}
}

func appendInsertions(edits *[]Edit, lines []string) {
	for _, line := range lines {
		*edits = append(*edits, Edit{Kind: Insert, Line: line})
	}
}

func patienceAnchors(before, after []string) []match {
	beforeCounts := make(map[string]int, len(before))
	afterCounts := make(map[string]int, len(after))
	afterPositions := make(map[string]int, len(after))
	for _, line := range before {
		beforeCounts[line]++
	}
	for index, line := range after {
		afterCounts[line]++
		afterPositions[line] = index
	}
	candidates := make([]match, 0, min(len(before), len(after)))
	for index, line := range before {
		if beforeCounts[line] == 1 && afterCounts[line] == 1 {
			candidates = append(candidates, match{before: index, after: afterPositions[line]})
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	tails := make([]int, 0, len(candidates))
	previous := make([]int, len(candidates))
	for index := range previous {
		previous[index] = -1
	}
	for index, candidate := range candidates {
		position := sort.Search(len(tails), func(tail int) bool {
			return candidates[tails[tail]].after >= candidate.after
		})
		if position > 0 {
			previous[index] = tails[position-1]
		}
		if position == len(tails) {
			tails = append(tails, index)
		} else {
			tails[position] = index
		}
	}
	anchors := make([]match, len(tails))
	for index, candidateIndex := len(anchors)-1, tails[len(tails)-1]; index >= 0; index-- {
		anchors[index] = candidates[candidateIndex]
		candidateIndex = previous[candidateIndex]
	}
	return anchors
}

func collectMatches(before, after []string, beforeOffset, afterOffset int, matches *[]match) {
	if len(before) == 0 || len(after) == 0 {
		return
	}
	if len(before) == 1 {
		for index, line := range after {
			if before[0] == line {
				*matches = append(*matches, match{before: beforeOffset, after: afterOffset + index})
				return
			}
		}
		return
	}
	middle := len(before) / 2
	left := prefixLengths(before[:middle], after)
	right := suffixLengths(before[middle:], after)
	split, best := 0, -1
	for index := 0; index <= len(after); index++ {
		if score := left[index] + right[index]; score > best {
			split, best = index, score
		}
	}
	collectMatches(before[:middle], after[:split], beforeOffset, afterOffset, matches)
	collectMatches(before[middle:], after[split:], beforeOffset+middle, afterOffset+split, matches)
}

func prefixLengths(before, after []string) []int {
	previous := make([]int, len(after)+1)
	current := make([]int, len(after)+1)
	for _, beforeLine := range before {
		current[0] = 0
		for index, afterLine := range after {
			if beforeLine == afterLine {
				current[index+1] = previous[index] + 1
			} else {
				current[index+1] = max(previous[index+1], current[index])
			}
		}
		previous, current = current, previous
	}
	return previous
}

func suffixLengths(before, after []string) []int {
	previous := make([]int, len(after)+1)
	current := make([]int, len(after)+1)
	for beforeIndex := len(before) - 1; beforeIndex >= 0; beforeIndex-- {
		current[len(after)] = 0
		for afterIndex := len(after) - 1; afterIndex >= 0; afterIndex-- {
			if before[beforeIndex] == after[afterIndex] {
				current[afterIndex] = previous[afterIndex+1] + 1
			} else {
				current[afterIndex] = max(previous[afterIndex], current[afterIndex+1])
			}
		}
		previous, current = current, previous
	}
	return previous
}

// ContextRanges returns zero-based half-open edit ranges containing every
// changed line plus at most context adjacent equal lines.
func ContextRanges(edits []Edit, context int) []Range {
	changed := make([]int, 0)
	for index, edit := range edits {
		if edit.Kind != Equal {
			changed = append(changed, index)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	ranges := make([]Range, 0, len(changed))
	current := Range{Start: max(0, changed[0]-context), End: min(len(edits), changed[0]+context+1)}
	for _, index := range changed[1:] {
		start := max(0, index-context)
		end := min(len(edits), index+context+1)
		if start <= current.End {
			current.End = max(current.End, end)
			continue
		}
		ranges = append(ranges, current)
		current = Range{Start: start, End: end}
	}
	return append(ranges, current)
}
