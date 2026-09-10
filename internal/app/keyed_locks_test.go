package app

import "testing"

func TestCandidateKeyedLocksRemoveIdleEntries(t *testing.T) {
	var locks keyedLocks
	for index := 0; index < 1_000; index++ {
		unlock := locks.lock(string(rune(index + 1)))
		unlock()
	}
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("idle Candidate locks retained: %d", len(locks.entries))
	}
}
