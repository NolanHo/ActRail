import { describe, expect, it } from "vitest";

import { getSessionDisplayName } from "./session-display";

describe("getSessionDisplayName", () => {
  it("preserves editable display names before the original title", () => {
    expect(getSessionDisplayName({
      session_id: "s_1",
      display_name: "Focused name",
      alias: "Alias",
      title: "Original title",
      cwd: "/root/code/ActRail",
    })).toBe("Focused name");

    expect(getSessionDisplayName({
      session_id: "s_1",
      alias: "Alias",
      title: "Original title",
      cwd: "/root/code/ActRail",
    })).toBe("Alias");
  });

  it("falls back to the cwd basename instead of the full path", () => {
    expect(getSessionDisplayName({
      session_id: "s_1",
      cwd: "/root/code/ActRail",
    })).toBe("ActRail");

    expect(getSessionDisplayName({
      session_id: "s_1",
      cwd: "C:\\Users\\nolan\\ActRail",
    })).toBe("ActRail");
  });
});
