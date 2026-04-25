package app

import (
	"fmt"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/config"
)

func newPersistentStubWithRuntime(cfg config.Config, now func() time.Time, runtimeCfg RuntimeConfig) (*Stub, error) {
	if err := cfg.Storage.EnsureDir(); err != nil {
		return nil, fmt.Errorf("ensure actrail data dir: %w", err)
	}
	catalog, err := sqlitestore.OpenSessionCatalog(cfg.SQLitePath())
	if err != nil {
		return nil, err
	}
	records, err := loadPersistedSessions(catalog)
	if err != nil {
		_ = catalog.Close()
		return nil, err
	}
	stub := &Stub{
		cfg:      cfg,
		registry: newSessionRegistry(now, catalog),
		launcher: newRuntimeLauncher(runtimeCfg),
	}
	if err := stub.registry.Rehydrate(records); err != nil {
		_ = catalog.Close()
		return nil, err
	}
	return stub, nil
}
