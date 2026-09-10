//go:build !darwin

package runtimeboundary

import (
	"context"
	"errors"
)

var errDarwinBoundaryUnavailable = errors.New("Darwin runtime boundary is unavailable; failing closed")

func Render(Config) ([]byte, error) {
	return nil, errDarwinBoundaryUnavailable
}

func Run(context.Context, Config, string) (ProbeReport, error) {
	return ProbeReport{}, errDarwinBoundaryUnavailable
}
