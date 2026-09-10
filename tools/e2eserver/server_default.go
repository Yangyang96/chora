//go:build !chora_e2e

package main

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/Yangyang96/chora/internal/localweb"
)

const ossAlphaClosureScenario = "oss-alpha-closure"

type roomServer interface {
	Close() error
	Handler() http.Handler
}

func newRoomServer(ctx context.Context, databasePath, webRoot, scenario string, logger *log.Logger) (roomServer, error) {
	if scenario != "" {
		return nil, errors.New("test-only e2eserver scenario requires the chora_e2e build tag")
	}
	return localweb.New(ctx, databasePath, webRoot, logger)
}
