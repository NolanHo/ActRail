import type { LaunchBackendDefaults } from "./types";

export function uniqueStrings(values: Array<string | null | undefined>) {
  const seen = new Set<string>();
  const result: string[] = [];
  for (const value of values) {
    if (typeof value !== "string") continue;
    const trimmed = value.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    result.push(trimmed);
  }
  return result;
}

export function providerChoicesForDefaults(defaults: LaunchBackendDefaults) {
  return uniqueStrings([...(defaults.provider_choices ?? []), defaults.provider_choice, ...(defaults.model_providers ?? [])]);
}

export function reasoningChoicesForDefaults(defaults: LaunchBackendDefaults, backend = "") {
  return uniqueStrings([
    ...(defaults.reasoning_efforts ?? []),
    defaults.reasoning_effort,
    ...(backend === "pi" ? ["off", "minimal", "low", "medium", "high", "xhigh"] : []),
  ]);
}

export function defaultProviderFor(defaults: LaunchBackendDefaults) {
  const choices = providerChoicesForDefaults(defaults);
  return defaults.provider_choice?.trim() || defaults.model_provider?.trim() || choices[0] || "";
}

export function defaultModelFor(defaults: LaunchBackendDefaults, backend: string, providerChoice: string) {
  if (backend === "pi") {
    return defaultPiModelForProvider(defaults, providerChoice);
  }
  const scopedModels = uniqueStrings(defaults.provider_models?.[providerChoice] ?? []);
  if (providerChoice && defaults.provider_choice !== providerChoice && scopedModels[0]) {
    return scopedModels[0];
  }
  return defaults.model?.trim() || modelChoicesForDefaults(defaults, backend, providerChoice)[0] || "";
}

export function defaultReasoningFor(defaults: LaunchBackendDefaults, backend: string) {
  const choices = reasoningChoicesForDefaults(defaults, backend);
  return defaults.reasoning_effort?.trim() || (backend === "pi" ? "high" : choices[0] || "");
}

export function modelChoicesForDefaults(defaults: LaunchBackendDefaults, backend: string, providerChoice: string) {
  const scopedModels = defaults.provider_models?.[providerChoice] ?? [];
  if (backend === "pi") {
    const configuredModel = defaults.provider_choice === providerChoice ? defaults.model : undefined;
    return uniqueStrings([...scopedModels, configuredModel]);
  }
  return uniqueStrings([...scopedModels, ...(defaults.models ?? []), defaults.model]);
}

export function defaultPiModelForProvider(defaults: LaunchBackendDefaults, providerChoice: string) {
  const scopedModels = uniqueStrings(defaults.provider_models?.[providerChoice] ?? []);
  if (defaults.provider_choice === providerChoice) {
    const configuredModel = defaults.model?.trim();
    if (configuredModel) {
      return configuredModel;
    }
  }
  return scopedModels[0] || "";
}
