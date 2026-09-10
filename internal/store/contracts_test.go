package store_test

import (
	"testing"

	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/store/sqlite"
)

func TestSQLiteImplementsStoreContract(t *testing.T) {
	var _ storecontract.Store = (*sqlite.Store)(nil)
}
