package localweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalApplicationsOnlyListInstalledSupportedBundles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Cursor.app", "Utilities/Terminal.app", "Other.app", "Nested/deeper/Zed.app"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Visual Studio Code.app"), []byte("not a bundle directory"), 0600); err != nil {
		t.Fatal(err)
	}
	apps := discoverExternalApplications([]string{root, root})
	if len(apps) != 2 || apps[0].ID != "cursor" || apps[1].ID != "terminal" {
		t.Fatalf("apps=%#v", apps)
	}
	encoded, _ := json.Marshal(apps)
	if strings.Contains(string(encoded), root) {
		t.Fatal("local application paths leaked in response")
	}
	if err := os.Remove(filepath.Join(root, "Cursor.app")); err != nil {
		t.Fatal(err)
	}
	if got := discoverExternalApplications([]string{root}); len(got) != 1 || got[0].ID != "terminal" {
		t.Fatalf("removed app still discovered: %#v", got)
	}
}

func TestExternalCatalogRemainsFixedArgv(t *testing.T) {
	for _, app := range externalApplicationCatalog {
		dir := "/tmp/a b; $(touch nope)"
		args, err := externalDirectoryArgs(app.ID, dir)
		if err != nil || len(args) != 3 || args[0] != "-a" || args[2] != dir {
			t.Fatalf("%s: %v %v", app.ID, args, err)
		}
	}
	for _, id := range []string{"/Applications/Other.app", "--args", "cursor;open /etc"} {
		if _, err := externalDirectoryArgs(id, "/tmp/repo"); err == nil {
			t.Fatal("accepted client executable", id)
		}
	}
}

func TestExternalOpeningRejectsCrossSiteAndNonJSON(t *testing.T) {
	server := &Server{}
	for _, test := range []struct {
		host, media, origin string
		status              int
	}{
		{"remote.example", "application/json", "", http.StatusForbidden},
		{"127.0.0.1:8787", "text/plain", "", http.StatusUnsupportedMediaType},
		{"127.0.0.1:8787", "application/json", "https://evil.example", http.StatusForbidden},
	} {
		r := newLocalRequest(http.MethodPost, "http://"+test.host+"/api/projects/project/open-external", strings.NewReader(`{"application":"terminal"}`))
		r.Header.Set("Content-Type", test.media)
		if test.origin != "" {
			r.Header.Set("Origin", test.origin)
		}
		w := httptest.NewRecorder()
		http.NewCrossOriginProtection().Handler(http.HandlerFunc(server.openExternalDirectory)).ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%+v got%d", test, w.Code)
		}
	}
}
