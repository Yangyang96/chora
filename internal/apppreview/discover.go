package apppreview

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var scriptPortPattern = regexp.MustCompile(`(?:^|[[:space:]=])(?:--port(?:[[:space:]=]+)|PORT=)([0-9]{1,5})(?:[[:space:]]|$)`)

func Discover(root string) ([]Suggestion, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("app preview discovery root must be absolute and clean")
	}
	path := filepath.Join(root, "package.json")
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("package.json is not a bounded regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("package.json is invalid")
	}
	var suggestions []Suggestion
	for _, name := range []string{"dev", "start"} {
		script, exists := manifest.Scripts[name]
		if !exists || script == "" {
			continue
		}
		port := 3000
		if name == "dev" {
			port = 5173
		}
		if match := scriptPortPattern.FindStringSubmatch(script); len(match) == 2 {
			if parsed, parseErr := strconv.Atoi(match[1]); parseErr == nil && parsed > 0 && parsed <= 65535 {
				port = parsed
			}
		}
		suggestions = append(suggestions, Suggestion{Name: name, Config: Config{Command: "npm run " + name, WorkingDirectory: ".", Port: port}})
	}
	return suggestions, nil
}
