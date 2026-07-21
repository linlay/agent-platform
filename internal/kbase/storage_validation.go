package kbase

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agent-platform/internal/sqlitecontract"
)

type storageValidator struct {
	resolver *capabilityResolver
	state    *capabilityState
}

func newStorageValidator(resolver *capabilityResolver, state *capabilityState) *storageValidator {
	return &storageValidator{resolver: resolver, state: state}
}

func (v *storageValidator) ValidateOwnership() error {
	owners := map[string]string{}
	for _, spec := range v.resolver.Specs() {
		storage := strings.ToLower(strings.TrimSpace(spec.Config.Storage.Location))
		if storage == "" {
			storage = "runtime"
		}
		var root string
		switch storage {
		case "runtime", "workspace":
			root = v.resolver.StorageDirForSpec(spec)
		default:
			continue
		}
		canonical := canonicalStoragePath(root)
		if owner, exists := owners[canonical]; exists && owner != spec.Key {
			return fmt.Errorf("KBASE storageDir %s is shared by agents %s and %s; each canonical storageDir must have exactly one owner", canonical, owner, spec.Key)
		}
		owners[canonical] = spec.Key
	}
	return nil
}

func (v *storageValidator) ValidateRuntimeContracts() error {
	for _, spec := range v.resolver.Specs() {
		storageDir := v.resolver.StorageDirForSpec(spec)
		dbPath := filepath.Join(storageDir, "control.db")
		if _, err := os.Stat(dbPath); err == nil {
			control, err := OpenReadControlStore(storageDir)
			if err != nil {
				return fmt.Errorf("KBASE storage schema agent=%s storageDir=%s: %w", spec.Key, storageDir, err)
			}
			if err := control.Close(); err != nil {
				return fmt.Errorf("close KBASE storage schema agent=%s storageDir=%s: %w", spec.Key, storageDir, err)
			}
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat KBASE control database agent=%s storageDir=%s: %w", spec.Key, storageDir, err)
		}
		residual, err := sqlitecontract.HasResidualData(storageDir)
		if err != nil {
			return fmt.Errorf("inspect KBASE storage agent=%s storageDir=%s: %w", spec.Key, storageDir, err)
		}
		if residual {
			return fmt.Errorf("KBASE storage schema agent=%s storageDir=%s: %w", spec.Key, storageDir,
				sqlitecontract.Unsupported(dbPath, storageDir, "control.db is missing but the storage directory contains residual data"))
		}
	}
	return nil
}

func (v *storageValidator) ValidateAndAdoptStartup() map[string]error {
	failures := map[string]error{}
	for _, spec := range v.resolver.Specs() {
		storageDir := v.resolver.StorageDirForSpec(spec)
		dbPath := filepath.Join(storageDir, "control.db")
		if _, err := os.Stat(dbPath); err == nil {
			control, openErr := OpenControlStoreAtStartup(storageDir)
			if openErr != nil {
				failures[spec.Key] = fmt.Errorf("KBASE storage schema agent=%s storageDir=%s: %w", spec.Key, storageDir, openErr)
				continue
			}
			if closeErr := control.Close(); closeErr != nil {
				failures[spec.Key] = fmt.Errorf("close KBASE storage schema agent=%s storageDir=%s: %w", spec.Key, storageDir, closeErr)
			}
			continue
		} else if !os.IsNotExist(err) {
			failures[spec.Key] = fmt.Errorf("stat KBASE control database agent=%s storageDir=%s: %w", spec.Key, storageDir, err)
			continue
		}
		residual, err := sqlitecontract.HasResidualData(storageDir)
		if err != nil {
			failures[spec.Key] = fmt.Errorf("inspect KBASE storage agent=%s storageDir=%s: %w", spec.Key, storageDir, err)
			continue
		}
		if residual {
			failures[spec.Key] = fmt.Errorf("KBASE storage schema agent=%s storageDir=%s: %w", spec.Key, storageDir,
				sqlitecontract.Unsupported(dbPath, storageDir, "control.db is missing but the storage directory contains residual data"))
		}
	}
	v.state.ReplaceFailures(failures)
	return failures
}
