package buildinfo

// Version can be overridden at build time with -ldflags.
var Version = "dev"

type Info struct {
	Version string
}

func Current() Info {
	return Info{Version: Version}
}
