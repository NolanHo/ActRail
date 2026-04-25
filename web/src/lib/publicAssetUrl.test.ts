import { describe, expect, it } from "vitest";
import { toPublicAssetUrl } from "./publicAssetUrl";

describe("toPublicAssetUrl", () => {
  it("keeps root deployments rooted at slash", () => {
    expect(toPublicAssetUrl("service-worker.js", "/")).toBe("/service-worker.js");
    expect(toPublicAssetUrl("/manifest.webmanifest", "/")).toBe("/manifest.webmanifest");
  });

  it("prefixes assets for nested deployments", () => {
    expect(toPublicAssetUrl("service-worker.js", "/actrail/")).toBe("/actrail/service-worker.js");
    expect(toPublicAssetUrl("/favicon.png", "/nested/app/")).toBe("/nested/app/favicon.png");
  });

  it("normalizes relative base URLs against the document base", () => {
    document.head.innerHTML = '<base href="/actrail/">';
    window.history.replaceState({}, "", "/actrail/");

    expect(toPublicAssetUrl("service-worker.js", "./")).toBe("/actrail/service-worker.js");
    expect(toPublicAssetUrl("/manifest.webmanifest", "./")).toBe("/actrail/manifest.webmanifest");
  });

  it("keeps relative bases anchored to the app base on deep routes", () => {
    document.head.innerHTML = '<base href="/actrail/">';
    window.history.replaceState({}, "", "/actrail/deep/nested/route");

    expect(toPublicAssetUrl("service-worker.js", "./")).toBe("/actrail/service-worker.js");
    expect(toPublicAssetUrl("/manifest.webmanifest", "./")).toBe("/actrail/manifest.webmanifest");
  });

  it("strips origins from absolute base URLs", () => {
    expect(toPublicAssetUrl("service-worker.js", "https://example.test/actrail/")).toBe("/actrail/service-worker.js");
    expect(toPublicAssetUrl("/favicon.png", "https://example.test/nested/app/")).toBe("/nested/app/favicon.png");
  });
});
