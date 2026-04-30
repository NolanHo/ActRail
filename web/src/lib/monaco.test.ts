import { describe, expect, it } from "vitest";

import { workerModuleKeyForLabel } from "./monaco";

describe("monaco loader", () => {
  it("uses the editor worker for all read-only Monaco views", () => {
    expect(workerModuleKeyForLabel("json")).toBe("editor");
    expect(workerModuleKeyForLabel("css")).toBe("editor");
    expect(workerModuleKeyForLabel("scss")).toBe("editor");
    expect(workerModuleKeyForLabel("html")).toBe("editor");
    expect(workerModuleKeyForLabel("javascript")).toBe("editor");
    expect(workerModuleKeyForLabel("typescript")).toBe("editor");
    expect(workerModuleKeyForLabel("markdown")).toBe("editor");
  });
});
