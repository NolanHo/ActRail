import { describe, expect, it } from "vitest";

import { backendSupportsReasoningEffort } from "./launch";

describe("backendSupportsReasoningEffort", () => {
  it("uses backend capability matrix when present", () => {
    const defaults = {
      backend_capabilities: {
        codex: { launch_reasoning_effort: true },
        pi: { launch_reasoning_effort: false },
      },
    };

    expect(backendSupportsReasoningEffort("codex", defaults)).toBe(true);
    expect(backendSupportsReasoningEffort("pi", defaults)).toBe(false);
  });

  it("falls back to legacy backend behavior without a matrix", () => {
    expect(backendSupportsReasoningEffort("codex")).toBe(false);
    expect(backendSupportsReasoningEffort("pi")).toBe(true);
  });
});
