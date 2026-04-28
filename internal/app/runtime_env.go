package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"actrail/internal/adapters/process"
	"actrail/internal/domain/session"
)

const codexAuthPath = ".codex/auth.json"

func defaultResolveLaunchEnv(backend session.Backend, env process.Environment) (process.Environment, error) {
	if backend != session.BackendCodex {
		return env, nil
	}
	return resolveCodexLaunchEnv(env, os.LookupEnv, os.UserHomeDir)
}

func resolveCodexLaunchEnv(env process.Environment, lookupEnv func(string) (string, bool), homeDir func() (string, error)) (process.Environment, error) {
	aliases := map[string]string{}
	crsKey, _ := resolvedEnvValue(env, lookupEnv, "CRS_OAI_KEY")
	openAIKey, _ := resolvedEnvValue(env, lookupEnv, "OPENAI_API_KEY")
	if openAIKey == "" || crsKey == "" {
		authKey, err := readCodexAuthOpenAIKey(homeDir)
		if err != nil {
			return process.Environment{}, err
		}
		if openAIKey == "" {
			openAIKey = authKey
		}
		if crsKey == "" {
			crsKey = authKey
		}
	}
	if openAIKey != "" {
		aliases["OPENAI_API_KEY"] = openAIKey
	}
	if crsKey != "" {
		aliases["CRS_OAI_KEY"] = crsKey
	}
	if len(aliases) == 0 {
		return env, nil
	}
	return mergeLaunchEnv(env, aliases)
}

func readCodexAuthOpenAIKey(homeDir func() (string, error)) (string, error) {
	root, err := homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir for codex auth: %w", err)
	}
	path := filepath.Join(root, codexAuthPath)
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read codex auth %q: %w", path, err)
	}
	var payload map[string]string
	if err := json.Unmarshal(blob, &payload); err != nil {
		return "", fmt.Errorf("decode codex auth %q: %w", path, err)
	}
	return strings.TrimSpace(payload["OPENAI_API_KEY"]), nil
}

func resolvedEnvValue(env process.Environment, lookupEnv func(string) (string, bool), key string) (string, bool) {
	for _, item := range env.Vars() {
		if item.Name() != key {
			continue
		}
		return item.Value(), item.Value() != ""
	}
	if lookupEnv == nil {
		return "", false
	}
	value, ok := lookupEnv(key)
	return strings.TrimSpace(value), ok && strings.TrimSpace(value) != ""
}

func mergeLaunchEnv(env process.Environment, extra map[string]string) (process.Environment, error) {
	vars := env.Vars()
	index := make(map[string]int, len(vars)+len(extra))
	for i, item := range vars {
		index[item.Name()] = i
	}
	for name, value := range extra {
		if strings.TrimSpace(value) == "" {
			continue
		}
		item, err := process.NewEnvVar(name, value)
		if err != nil {
			return process.Environment{}, err
		}
		if pos, ok := index[name]; ok {
			vars[pos] = item
			continue
		}
		index[name] = len(vars)
		vars = append(vars, item)
	}
	switch env.Mode() {
	case process.EnvModeReplace:
		return process.ReplaceEnv(vars...)
	default:
		return process.InheritEnv(vars...)
	}
}
