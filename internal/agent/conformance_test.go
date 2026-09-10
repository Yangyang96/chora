package agent_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/agent/fake"
)

func TestAgentPackageHasNoProcessSupervisorImports(t *testing.T) {
	forbidden := []string{`"os/exec"`, `"syscall"`, `"golang.org/x/sys/unix"`, `"github.com/Yangyang96/chora/internal/supervisor"`}
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range file.Imports {
			for _, bad := range forbidden {
				if imported.Path.Value == bad {
					t.Fatalf("%s imports %s", path, bad)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFakeAdapterExportsNoProcessLifecycleMethods(t *testing.T) {
	typ := reflect.TypeOf(&fake.Adapter{})
	for _, name := range []string{"Start", "Stop", "Kill"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Fatalf("fake adapter exports %s", name)
		}
	}
}
