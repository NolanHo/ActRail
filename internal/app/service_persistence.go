package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	sqlitestore "actrail/internal/adapters/sqlite"
	"actrail/internal/config"
	"actrail/internal/domain/session"
)

func newPersistentStubWithRuntime(cfg config.Config, now func() time.Time, runtimeCfg RuntimeConfig) (*Stub, error) {
	if err := cfg.Storage.EnsureDir(); err != nil {
		return nil, fmt.Errorf("ensure actrail data dir: %w", err)
	}
	if strings.TrimSpace(runtimeCfg.IODRuntimeRoot) == "" {
		runtimeCfg.IODRuntimeRoot = cfg.Storage.IODRuntimeRoot()
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
	sourceRefs, err := catalog.ListSessionSourceRefs(context.Background())
	if err != nil {
		_ = catalog.Close()
		return nil, err
	}
	records = applyImportedSourceRefs(records, sourceRefs)
	recentCwds, cwdGroups, err := loadPersistedAppState(catalog)
	if err != nil {
		_ = catalog.Close()
		return nil, err
	}
	subagentSnapshots, err := catalog.ListSubagentSnapshots(context.Background())
	if err != nil {
		_ = catalog.Close()
		return nil, err
	}
	stub := &Stub{
		cfg:                 cfg,
		registry:            newSessionRegistry(now, catalog),
		launcher:            newRuntimeLauncher(runtimeCfg),
		appStore:            catalog,
		helperDialer:        runtimeCfg.IODDialer,
		helperBindings:      newHelperBindingStore(cfg.Storage.IODBindingsDir()),
		helpers:             newHelperRegistry(),
		messageCache:        newSessionMessageCache(defaultSessionMessageCacheEntries),
		waitStore:           catalog,
		supervisorStore:     catalog,
		subagents:           newSubagentRegistry(now, catalog),
		runtimeAgentRunning: map[session.SessionID]bool{},
		piRPCStates:         map[session.SessionID]piRPCStateCache{},
		piModels:            piModelCache{},
		recentCwds:          recentCwds,
		cwdGroups:           cwdGroups,
	}
	if err := stub.registry.Rehydrate(records); err != nil {
		_ = catalog.Close()
		return nil, err
	}
	if err := stub.subagents.rehydrate(subagentSnapshots); err != nil {
		_ = catalog.Close()
		return nil, err
	}
	for _, record := range records {
		if record.runtimeAgentRunning {
			stub.runtimeAgentRunning[record.identity.SessionID()] = true
		}
	}
	if err := stub.reattachSurvivingHelpers(context.Background()); err != nil {
		_ = catalog.Close()
		return nil, err
	}
	stub.reconcilePersistedBusySessions(context.Background())
	for _, record := range stub.registry.List() {
		stub.startRuntimeIngest(record.identity.SessionID(), record.identity.Backend(), stub.runtimeForSession(record.identity.SessionID(), record.identity.Backend(), record.runtime))
	}
	return stub, nil
}
