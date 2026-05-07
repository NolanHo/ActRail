package app

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const codexConfigPath = ".codex/config.toml"

type codexConfig struct {
	Model                string
	ModelProvider        string
	PreferredAuthMethod  string
	ModelReasoningEffort string
	Providers            []string
	ProviderModels       map[string][]string
}

func readCodexConfig() codexConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		return codexConfig{}
	}
	return readCodexConfigPath(filepath.Join(home, codexConfigPath))
}

func readCodexConfigPath(path string) codexConfig {
	file, err := os.Open(path)
	if err != nil {
		return codexConfig{}
	}
	defer file.Close()

	cfg := codexConfig{ProviderModels: map[string][]string{}}
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := stripTOMLComment(strings.TrimSpace(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.Trim(line, "[]"))
			if provider, ok := strings.CutPrefix(section, "model_providers."); ok {
				provider = strings.Trim(strings.TrimSpace(provider), `"`)
				if provider != "" {
					cfg.Providers = append(cfg.Providers, provider)
				}
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		parsedValue := parseTOMLString(strings.TrimSpace(value))
		if strings.HasPrefix(section, "model_providers.") {
			provider := strings.Trim(strings.TrimSpace(strings.TrimPrefix(section, "model_providers.")), `"`)
			if provider != "" && key == "models" {
				cfg.ProviderModels[provider] = parseTOMLStringArray(strings.TrimSpace(value))
			}
			continue
		}
		if section != "" {
			continue
		}
		switch key {
		case "model":
			cfg.Model = parsedValue
		case "model_provider":
			cfg.ModelProvider = parsedValue
			if parsedValue != "" {
				cfg.Providers = append(cfg.Providers, parsedValue)
			}
		case "preferred_auth_method":
			cfg.PreferredAuthMethod = parsedValue
		case "model_reasoning_effort":
			cfg.ModelReasoningEffort = parsedValue
		}
	}
	cfg.Providers = uniqueSortedStrings(cfg.Providers)
	for provider, models := range cfg.ProviderModels {
		cfg.ProviderModels[provider] = uniqueSortedStrings(models)
	}
	return cfg
}

func stripTOMLComment(line string) string {
	inString := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inString {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if r == '#' && !inString {
			return strings.TrimSpace(line[:i])
		}
	}
	return strings.TrimSpace(line)
}

func parseTOMLString(value string) string {
	if value == "" {
		return ""
	}
	if parsed, err := strconv.Unquote(value); err == nil {
		return strings.TrimSpace(parsed)
	}
	return strings.Trim(strings.TrimSpace(value), `"`)
}

func parseTOMLStringArray(value string) []string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return nil
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
	if inner == "" {
		return nil
	}
	out := []string{}
	for _, part := range splitTOMLArray(inner) {
		if parsed := parseTOMLString(part); parsed != "" {
			out = append(out, parsed)
		}
	}
	sort.Strings(out)
	return uniqueSortedStrings(out)
}

func splitTOMLArray(value string) []string {
	parts := []string{}
	start := 0
	inString := false
	escaped := false
	for i, r := range value {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inString {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if r == ',' && !inString {
			parts = append(parts, strings.TrimSpace(value[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}
