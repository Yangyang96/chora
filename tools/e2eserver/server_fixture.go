//go:build chora_e2e

package main

import (
	"context"
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
	if scenario == ossAlphaClosureScenario {
		return newOSSAlphaClosureServer(ctx, databasePath, webRoot, logger)
	}
	return localweb.NewE2E(ctx, databasePath, webRoot, logger)
}
