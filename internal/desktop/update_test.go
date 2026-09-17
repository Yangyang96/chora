package desktop

import "testing"

func TestReleaseNewer(t *testing.T) {
	tests := []struct {
		candidate, current string
		want               bool
	}{{"0.1.0-alpha.2", "0.1.0-alpha.1", true}, {"1.0.1", "1.0.0", true}, {"1.2.0", "1.10.0", false}, {"1.0.0", "1.0.0-alpha.3", true}, {"1.0.0+2", "1.0.0+1", false}}
	for _, test := range tests {
		got, err := releaseNewer(test.candidate, test.current)
		if err != nil || got != test.want {
			t.Fatalf("versionNewer(%q,%q)=%v,%v", test.candidate, test.current, got, err)
		}
	}
	if _, err := releaseNewer("1.beta", "1.0.0"); err == nil {
		t.Fatal("accepted nonnumeric version")
	}
}
