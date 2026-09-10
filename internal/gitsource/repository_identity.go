package gitsource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

const repositoryPhysicalIdentityVersion = "chora.repository-physical-identity.v1"

// ProveRevision verifies an immutable historical commit/tree pair without
// requiring the checkout's current HEAD to remain at that commit. This is used
// when legacy repository identity is established lazily after migration.
func ProveRevision(ctx context.Context, root, commit, tree string) error {
	if !validObjectID(commit) || !validObjectID(tree) {
		return ErrRevisionDrift
	}
	resolvedCommit, err := gitOutput(ctx, root, "rev-parse", "--verify", commit+"^{commit}")
	if err != nil {
		if operationalGitError(err) {
			return err
		}
		return ErrRevisionDrift
	}
	if strings.TrimSpace(resolvedCommit) != commit {
		return ErrRevisionDrift
	}
	resolvedTree, err := gitOutput(ctx, root, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		if operationalGitError(err) {
			return err
		}
		return ErrRevisionDrift
	}
	if strings.TrimSpace(resolvedTree) != tree {
		return ErrRevisionDrift
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

// repositoryPhysicalIdentity binds a checkout to both its canonical location
// and stable filesystem objects. Mutable Git state such as HEAD, branch names,
// remotes, directory timestamps, and sizes deliberately does not participate.
func repositoryPhysicalIdentity(canonicalCheckout, canonicalCommonGitDir string) (string, error) {
	checkout, err := os.Stat(canonicalCheckout)
	if err != nil {
		return "", &PathError{Path: canonicalCheckout, Err: err}
	}
	common, err := os.Stat(canonicalCommonGitDir)
	if err != nil {
		return "", &PathError{Path: canonicalCommonGitDir, Err: err}
	}
	checkoutDevice, checkoutInode, err := deviceAndInode(checkout)
	if err != nil {
		return "", fmt.Errorf("inspect checkout identity: %w", err)
	}
	commonDevice, commonInode, err := deviceAndInode(common)
	if err != nil {
		return "", fmt.Errorf("inspect Git directory identity: %w", err)
	}

	digest := sha256.New()
	for _, field := range []string{
		repositoryPhysicalIdentityVersion,
		canonicalCheckout,
		canonicalCommonGitDir,
		checkoutDevice,
		checkoutInode,
		commonDevice,
		commonInode,
	} {
		_, _ = digest.Write([]byte(field))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// deviceAndInode uses reflection so this package remains buildable on hosts
// whose os.FileInfo.Sys value is not syscall.Stat_t. Supported local Alpha
// hosts expose numeric Dev and Ino fields; an unknown filesystem fails closed.
func deviceAndInode(info os.FileInfo) (string, string, error) {
	if info == nil || info.Sys() == nil {
		return "", "", errors.New("filesystem identity is unavailable")
	}
	value := reflect.ValueOf(info.Sys())
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", "", errors.New("filesystem identity is unavailable")
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return "", "", errors.New("filesystem identity has an unsupported representation")
	}
	device, ok := numericIdentityField(value.FieldByName("Dev"))
	if !ok {
		return "", "", errors.New("filesystem device identity is unavailable")
	}
	inode, ok := numericIdentityField(value.FieldByName("Ino"))
	if !ok {
		return "", "", errors.New("filesystem inode identity is unavailable")
	}
	return device, inode, nil
}

func numericIdentityField(value reflect.Value) (string, bool) {
	if !value.IsValid() {
		return "", false
	}
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(value.Uint(), 10), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Int() < 0 {
			return "", false
		}
		return strconv.FormatInt(value.Int(), 10), true
	default:
		return "", false
	}
}
