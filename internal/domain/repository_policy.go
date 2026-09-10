package domain

import "time"

// RepositoryPolicy bounds operations, never total repository admission size.
const (
	RepositoryPageSize           = 200
	RepositoryMaxPageSize        = 500
	RepositoryMetadataBytes      = 1 << 20
	RepositoryTextBytes          = 8 << 20
	TaskPatchBytes               = 100 << 20
	TaskChangedEntryLimit        = 4096
	TaskRepositoryLimit          = 16
	RepositoryMetadataTimeout    = 10 * time.Second
	RepositoryPreparationTimeout = 120 * time.Second
	RepositoryDiskReserveBytes   = 1 << 30
)
