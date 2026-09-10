package sqlite

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/domain"
	modernsqlite "modernc.org/sqlite"
)

func init() {
	modernsqlite.MustRegisterDeterministicScalarFunction("chora_room_revision_digest", 9, roomRevisionDigestFunction)
	modernsqlite.MustRegisterDeterministicScalarFunction("chora_valid_trusted_text", 3, trustedTextValidationFunction)
	modernsqlite.MustRegisterDeterministicScalarFunction("chora_valid_revision_provenance", 1, revisionProvenanceValidationFunction)
}

func trustedTextValidationFunction(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	var value string
	switch encoded := args[0].(type) {
	case string:
		value = encoded
	case []byte:
		value = string(encoded)
	default:
		return int64(0), nil
	}
	maxRunes, ok := args[1].(int64)
	if !ok || maxRunes < 0 || maxRunes > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("trusted text maximum is invalid")
	}
	required, ok := args[2].(int64)
	if !ok || (required != 0 && required != 1) {
		return nil, fmt.Errorf("trusted text required flag is invalid")
	}
	if domain.ValidTrustedContextText(value, int(maxRunes), required == 1) {
		return int64(1), nil
	}
	return int64(0), nil
}

func revisionProvenanceValidationFunction(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	var encoded []byte
	switch value := args[0].(type) {
	case string:
		encoded = []byte(value)
	case []byte:
		encoded = value
	default:
		return int64(0), nil
	}
	if !utf8.Valid(encoded) {
		return int64(0), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var provenance domain.RevisionProvenance
	if err := decoder.Decode(&provenance); err != nil {
		return int64(0), nil
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return int64(0), nil
	}
	if !provenance.Valid() {
		return int64(0), nil
	}
	return int64(1), nil
}

func roomRevisionDigestFunction(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	values := make([]string, len(args))
	for index, value := range args {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("room revision digest argument %d is not text", index)
		}
		values[index] = text
	}
	revisionID, err := domain.ParseContextRevisionID(values[0])
	if err != nil {
		return nil, err
	}
	roomID, err := domain.ParseRoomID(values[1])
	if err != nil {
		return nil, err
	}
	entryID, err := domain.ParseContextEntryID(values[2])
	if err != nil {
		return nil, err
	}
	confirmedAt, err := time.Parse(time.RFC3339Nano, values[7])
	if err != nil {
		return nil, err
	}
	revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{EntryID: entryID, RevisionID: revisionID, RoomID: roomID, Kind: domain.ContextKind(values[3]), RevisionNumber: 1, Title: values[4], Body: values[5], Locator: values[6], CreatedAt: confirmedAt, UpdatedAt: confirmedAt})
	if err != nil {
		return nil, err
	}
	record, err := domain.NewRoomRevision(revision, domain.HumanRoomProvenance(values[8]), confirmedAt)
	if err != nil {
		return nil, err
	}
	digest := record.Digest()
	return digest[:], nil
}
