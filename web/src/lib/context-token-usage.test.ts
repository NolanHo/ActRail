import { describe, expect, it } from "vitest";
import { calculateContextTokenUsage } from "./context-token-usage";

const messages = [
  { role: "system", text: "Follow the rules." },
  { role: "user", text: "Read package.json" },
  { type: "tool", name: "read", text: "package contents" },
  { type: "tool_result", toolName: "read", text: "{\"name\":\"app\"}" },
  { role: "assistant", text: "The package is an app." },
];

describe("calculateContextTokenUsage", () => {
  it("uses chars/4 estimates without loading a tokenizer chunk", async () => {
    const usage = await calculateContextTokenUsage(messages, "gpt-4o-mini");

    expect(usage.fallback).toBe(true);
    expect(usage.fallbackReason).toContain("chars/4");
    expect(usage.buckets.systemPrompt.tokens).toBeGreaterThan(0);
    expect(usage.buckets.tool.tokens).toBeGreaterThan(0);
    expect(usage.buckets.user.tokens).toBeGreaterThan(0);
    expect(usage.buckets.assist.tokens).toBeGreaterThan(0);
    expect(usage.totalTokens).toBe(
      usage.buckets.systemPrompt.tokens
      + usage.buckets.tool.tokens
      + usage.buckets.user.tokens
      + usage.buckets.assist.tokens,
    );
  });

  it("falls back to chars/4 when the model is unknown", async () => {
    const usage = await calculateContextTokenUsage([{ role: "user", text: "12345678" }], "unknown-model");

    expect(usage.fallback).toBe(true);
    expect(usage.fallbackReason).toContain("chars/4");
    expect(usage.buckets.user.tokens).toBe(2);
  });
});
