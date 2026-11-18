package dockersupervisor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestBudgetedPlanPreservesMasksAndRejectsExpiredRecovery(t *testing.T) {
	now := time.Now().UTC()
	d := strings.Repeat("a", 64)
	b := domain.RestrictedLaunch{Schema: "chora.restricted-launch.v2", Authorization: "auth", Workflow: "workflow", Project: "project", Task: "task", Charter: "charter", Run: "run", Attempt: "attempt", Actor: "actor", Gate: "gate", Operation: "op", InputDigest: d, AuthorityDigest: d, LaunchToken: "launch", Channel: "channel", SourceDigest: d, ConfigDigest: d, Epoch: 1, AuthorizedAt: now, ObservedAt: now, Deadline: now.Add(time.Minute)}
	p := BudgetedPlan{Bound: b, ImageID: "sha256:" + d, Container: "test-container", WorkspaceVolume: "test-volume", ContextDirectory: "/fixture/context", ReadOnlyMounts: map[string]string{"/workspace/repository/readonly": "/fixture/readonly"}}
	c, err := p.Create(now)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Join(c.Args, " ")
	for _, want := range []string{"--network none", "--pid private", "--ipc private", "--restart no", "--user 0:0", "--cap-drop ALL", "--read-only", GuardianPath, "dst=/workspace/repository/readonly,readonly", b.Deadline.Format(time.RFC3339Nano)} {
		if !strings.Contains(raw, want) {
			t.Fatal("missing restriction", want)
		}
	}
	for _, forbidden := range []string{"bridge", "auth.json", "docker.sock", "HTTP_PROXY", "--privileged", "sleep infinity"} {
		if strings.Contains(raw, forbidden) {
			t.Fatal("unsafe plan", forbidden)
		}
	}
	// This is an inspect document fixture, never an Engine qualification.
	h := map[string]any{"NetworkMode": "none", "ReadonlyRootfs": true, "NanoCpus": int64(2_000_000_000), "Memory": int64(4096 << 20), "MemorySwap": int64(4096 << 20), "PidsLimit": 256, "PidMode": "private", "IpcMode": "private", "RestartPolicy": map[string]string{"Name": "no"}, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "LogConfig": map[string]string{"Type": "none"}, "Ulimits": []any{map[string]any{"Name": "nofile", "Soft": 1024, "Hard": 1024}}, "Tmpfs": map[string]string{"/tmp": "rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=64m", "/run/chora/pi": "rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=16m"}}
	config := map[string]any{"User": "0:0", "Env": []string{"PATH=/usr/local/bin:/usr/bin:/bin"}, "Entrypoint": []string{GuardianPath}, "Cmd": []string{b.AuthorizedAt.Format(time.RFC3339Nano), b.Deadline.Format(time.RFC3339Nano)}}
	doc := map[string]any{"Id": d, "Name": "/" + p.Container, "Image": p.ImageID, "State": map[string]bool{"Running": true}, "Config": config, "HostConfig": h, "Mounts": []any{map[string]any{"Type": "volume", "Name": p.WorkspaceVolume, "Destination": "/workspace", "RW": true}, map[string]any{"Type": "bind", "Source": p.ContextDirectory, "Destination": "/input/context", "RW": false}, map[string]any{"Type": "bind", "Source": "/fixture/readonly", "Destination": "/workspace/repository/readonly", "RW": false}}, "NetworkSettings": map[string]any{"Networks": map[string]any{"none": map[string]any{}}}}
	rawInspect, _ := json.Marshal(doc)
	if err = p.VerifyEffective(rawInspect, d, now); err != nil {
		t.Fatal("safe fixture refused", err)
	}
	for key, bad := range map[string]any{"NetworkMode": "bridge", "Privileged": true, "CapAdd": []string{"SYS_ADMIN"}, "RestartPolicy": map[string]string{"Name": "always"}, "PidMode": "host"} {
		previous, existed := h[key]
		h[key] = bad
		rawInspect, _ = json.Marshal(doc)
		if p.VerifyEffective(rawInspect, d, now) == nil {
			t.Fatal("effective drift accepted", key)
		}
		if existed {
			h[key] = previous
		} else {
			delete(h, key)
		}
	}
	if _, err = p.Create(b.Deadline); err == nil {
		t.Fatal("expired recovery created container")
	}
	if _, err = p.Create(now.Add(-time.Second)); err == nil {
		t.Fatal("clock rollback created container")
	}
	p.ReadOnlyMounts = map[string]string{"/workspace/repository/../escape": "/fixture/ro"}
	if _, err = p.Create(now); err == nil {
		t.Fatal("mask escaped repository")
	}
}
