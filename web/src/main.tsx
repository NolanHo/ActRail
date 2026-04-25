import { render } from "preact";
import App from "./app/App";
import "./styles/index.css";
import { toPublicAssetUrl } from "./lib/publicAssetUrl";
import { installViewportCssVars } from "./lib/viewport";
import { applyThemeMode, readThemeMode } from "./app/app-shell/utils";

installViewportCssVars();
applyThemeMode(readThemeMode());
if ("serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register(toPublicAssetUrl("service-worker.js")).catch(() => undefined);
  });
}
render(<App />, document.getElementById("root")!);
