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

type persistentStubOptions struct {
	DeferRuntimeRestore bool
}

func newPersistentStubWithRuntime(cfg config.Config, now func() time.Time, runtimeCfg RuntimeConfig) (*Stub, error) {
	return newPersistentStubWithRuntimeOptions(cfg, now, runtimeCfg, persistentStubOptions{})
}

func newPersistentStubWithRuntimeOptions(cfg config.Config, now func() time.Time, runtimeCfg RuntimeConfig, options persistentStubOptions) (*Stub, error) {
	if err := cfg.Storage.EnsureDir(); err != nil {
		return nil, fmt.Errorf("ensure actrail data dir: %w", err)
	}
	runtimeCfg = runtimeConfigFromAppConfig(cfg, runtimeCfg)
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
	teamSnapshots, err := catalog.ListTeamSnapshots(context.Background())
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
		waitBlockers:        map[string]waitBlocker{},
		supervisorStore:     catalog,
		schedulerStore:      catalog,
		teams:               newTeamRegistry(now, catalog),
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
	if err := stub.teams.rehydrate(teamSnapshots); err != nil {
		_ = catalog.Close()
		return nil, err
	}
	if err := stub.orphanActiveWaits(context.Background(), nil); err != nil {
		_ = catalog.Close()
		return nil, err
	}
	for _, record := range records {
		if record.runtimeAgentRunning {
			stub.runtimeAgentRunning[record.identity.SessionID()] = true
		}
	}
	if options.DeferRuntimeRestore {
		return stub, nil
	}
	if err := stub.RestoreSurvivingRuntimes(context.Background()); err != nil {
		_ = catalog.Close()
		return nil, err
	}
	return stub, nil
}

func (s *Stub) RestoreSurvivingRuntimes(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if err := s.reattachSurvivingRuntimes(ctx); err != nil {
		return err
	}
	s.reconcilePersistedBusySessions(ctx)
	for _, record := range s.registry.List() {
		s.startRuntimeIngest(record.identity.SessionID(), record.identity.Backend(), s.runtimeForSession(record.identity.SessionID(), record.identity.Backend(), record.runtime))
	}
	return nil
}
