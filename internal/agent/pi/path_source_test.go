package pi

import (
	"path/filepath"
	"testing"
)

func TestNewPathPiSourceAcceptsCompatibleVersion(t *testing.T) {
	sha := [32]byte{1}
	source, err := NewPathPiSource(PathPiSourceParams{
		ExecutablePath: "/usr/local/bin/pi", Version: "0.85.0", ExecutableSHA256: sha,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !source.Configured() || source.Version() != "0.85.0" || source.ExecutableSHA256() != sha {
		t.Fatalf("source=%#v", source)
	}
}

func TestNewPathPiSourceRejectsBelowFloor(t *testing.T) {
	sha := [32]byte{1}
	if _, err := NewPathPiSource(PathPiSourceParams{
		ExecutablePath: "/usr/local/bin/pi", Version: "0.83.0", ExecutableSHA256: sha,
	}); err == nil {
		t.Fatal("expected error for below-floor version")
	}
}

func TestNewPathPiSourceValidation(t *testing.T) {
	sha := [32]byte{1}
	cases := []struct {
		name   string
		params PathPiSourceParams
	}{
		{"relative path", PathPiSourceParams{ExecutablePath: "bin/pi", Version: "0.84.2", ExecutableSHA256: sha}},
		{"unclean path", PathPiSourceParams{ExecutablePath: "/usr/local/bin/pi/", Version: "0.84.2", ExecutableSHA256: sha}},
		{"garbage version", PathPiSourceParams{ExecutablePath: "/usr/local/bin/pi", Version: "nope", ExecutableSHA256: sha}},
		{"zero sha", PathPiSourceParams{ExecutablePath: "/usr/local/bin/pi", Version: "0.84.2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPathPiSource(tc.params); err == nil {
				t.Fatalf("NewPathPiSource(%+v) accepted invalid params", tc.params)
			}
		})
	}
}

func TestPathPiSourceRoundTripAndDrift(t *testing.T) {
	sha := [32]byte{2}
	source, err := NewPathPiSource(PathPiSourceParams{
		ExecutablePath: filepath.Clean("/usr/local/bin/pi"), Version: "0.84.2", ExecutableSHA256: sha,
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestorePathPiSource(source.Record())
	if err != nil {
		t.Fatal(err)
	}
	if restored != source {
		t.Fatalf("round trip drift: %#v != %#v", restored, source)
	}
	record := source.Record()
	record.Version = "0.84.3"
	if _, err := RestorePathPiSource(record); err == nil {
		t.Fatal("drifted record accepted")
	}
}
