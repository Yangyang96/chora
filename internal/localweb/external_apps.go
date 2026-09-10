package localweb

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

type externalApplication struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"-"`
}

type applicationDefinition struct{ ID, Name, Kind, Bundle string }

var externalApplicationCatalog = []applicationDefinition{
	{"editor", "VS Code", "editor", "Visual Studio Code.app"},
	{"cursor", "Cursor", "editor", "Cursor.app"},
	{"windsurf", "Windsurf", "editor", "Windsurf.app"},
	{"zed", "Zed", "editor", "Zed.app"},
	{"sublime", "Sublime Text", "editor", "Sublime Text.app"},
	{"goland", "GoLand", "editor", "GoLand.app"},
	{"idea", "IntelliJ IDEA", "editor", "IntelliJ IDEA.app"},
	{"webstorm", "WebStorm", "editor", "WebStorm.app"},
	{"pycharm", "PyCharm", "editor", "PyCharm.app"},
	{"trae", "Trae", "editor", "Trae.app"},
	{"terminal", "Terminal", "terminal", "Terminal.app"},
	{"iterm", "iTerm", "terminal", "iTerm.app"},
	{"warp", "Warp", "terminal", "Warp.app"},
	{"ghostty", "Ghostty", "terminal", "Ghostty.app"},
}

func externalApplicationDefinition(id string) (applicationDefinition, bool) {
	for _, app := range externalApplicationCatalog {
		if app.ID == id {
			return app, true
		}
	}
	return applicationDefinition{}, false
}

// Search only standard app folders, including one organizational subfolder.
// Never inspect project files or accept executable paths from the browser.
func discoverExternalApplications(roots []string) []externalApplication {
	found := map[string]string{}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		paths := []string{root}
		for _, entry := range entries {
			if entry.IsDir() && filepath.Ext(entry.Name()) != ".app" {
				paths = append(paths, filepath.Join(root, entry.Name()))
			}
		}
		for _, dir := range paths {
			for _, definition := range externalApplicationCatalog {
				if found[definition.ID] != "" {
					continue
				}
				path := filepath.Join(dir, definition.Bundle)
				if info, err := os.Stat(path); err == nil && info.IsDir() {
					found[definition.ID] = path
				}
			}
		}
	}
	apps := []externalApplication{}
	for _, definition := range externalApplicationCatalog {
		if path := found[definition.ID]; path != "" {
			apps = append(apps, externalApplication{ID: definition.ID, Name: definition.Name, Kind: definition.Kind, Path: path})
		}
	}
	return apps
}

func installedExternalApplications() []externalApplication {
	if runtime.GOOS != "darwin" {
		return []externalApplication{}
	}
	roots := []string{"/Applications", "/System/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, "Applications"))
	}
	return discoverExternalApplications(roots)
}

func installedApplicationPath(id string) (string, error) {
	for _, app := range installedExternalApplications() {
		if app.ID == id {
			return app.Path, nil
		}
	}
	return "", errors.New("application is no longer installed; refresh the available applications")
}
