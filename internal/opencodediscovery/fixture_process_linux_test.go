package opencodediscovery

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func observeFixtureProcess(pid int) fixtureProcessObservation {
	r := fixtureProcessObservation{PID: pid, Presence: "unknown"}
	if pid <= 1 {
		r.QueryError = "invalid fixture PID"
		return r
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		r.Presence = "absent"
		return r
	}
	if err != nil {
		r.QueryError = err.Error()
		return r
	}
	// Exclude comm (which can contain spaces/parentheses) from both parsing and logs.
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		r.QueryError = "invalid process stat"
		return r
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		r.QueryError = "incomplete process stat"
		return r
	}
	ppid, parentErr := strconv.Atoi(fields[1])
	pgid, groupErr := strconv.Atoi(fields[2])
	start, startErr := strconv.ParseUint(fields[19], 10, 64)
	if parentErr != nil || groupErr != nil || startErr != nil || start == 0 || pgid <= 0 {
		r.QueryError = "invalid process identity fields"
		return r
	}
	r.Presence, r.PPID, r.PGID = "present", ppid, pgid
	r.Start, r.State = fields[19], fields[0]
	return r
}
