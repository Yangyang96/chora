package nativecapabilities

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestConfigSaveUsesCASAndKeepsProjectsIsolated(t *testing.T) {
	root := t.TempDir()
	firstID, secondID := domain.NewProjectID().String(), domain.NewProjectID().String()
	first, err := Save(root, firstID, 0, Config{SkillPaths: []string{"/opt/skills/one"}, DisabledMCPServers: []string{"legacy"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 {
		t.Fatalf("version=%d", first.Version)
	}
	if _, err := Save(root, firstID, 0, Config{SkillPaths: []string{"/opt/skills/stale"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save error=%v", err)
	}
	second, err := Save(root, secondID, 0, Config{MCPConfigPath: "/etc/chora/mcp.json"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Read(root, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, first) || second.Version != 1 || second.MCPConfigPath != "/etc/chora/mcp.json" {
		t.Fatalf("first=%+v loaded=%+v second=%+v", first, loaded, second)
	}
	info, err := os.Stat(filepath.Join(root, "native-capabilities", firstID+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private config mode=%v error=%v", info.Mode().Perm(), err)
	}
}

func TestConfigReadMissingReturnsExplicitEmptyDefault(t *testing.T) {
	config, err := Read(t.TempDir(), domain.NewProjectID().String())
	if err != nil {
		t.Fatal(err)
	}
	if config.Version != 0 || config.SkillPaths == nil || config.DisabledSkillPaths == nil || config.DisabledMCPServers == nil {
		t.Fatalf("config=%+v", config)
	}
}

func TestConfigRejectsTraversalAndSymlinkedStorage(t *testing.T) {
	root := t.TempDir()
	validID := domain.NewProjectID().String()
	for _, invalid := range []string{"../../outside", "project_invalid"} {
		if _, err := Read(root, invalid); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("project ID %q error=%v", invalid, err)
		}
	}
	if _, err := Save(root, validID, 0, Config{SkillPaths: []string{"/opt/skills/../escape"}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unclean path error=%v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "native-capabilities")); err != nil {
		t.Fatal(err)
	}
	if _, err := Save(root, validID, 0, Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("symlinked directory error=%v", err)
	}

	fileRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(fileRoot, "native-capabilities"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "config.json"), filepath.Join(fileRoot, "native-capabilities", validID+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(fileRoot, validID); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("symlinked config error=%v", err)
	}
}
