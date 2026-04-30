declare global {
  interface Window {
    MonacoEnvironment?: {
      getWorker?: (_workerId: string, label: string) => Worker;
    };
  }
}

import type * as Monaco from "monaco-editor/esm/vs/editor/editor.api.js";

type MonacoModule = typeof Monaco;
type WorkerFactory = { default: new () => Worker };
type WorkerModuleKey = "editor";

let configured = false;
let monacoPromise: Promise<MonacoModule> | null = null;
let workerFactoriesPromise: Promise<Record<WorkerModuleKey, WorkerFactory["default"]>> | null = null;

export function workerModuleKeyForLabel(_label: string): WorkerModuleKey {
  return "editor";
}

async function loadWorkerFactories() {
  if (!workerFactoriesPromise) {
    workerFactoriesPromise = import("monaco-editor/esm/vs/editor/editor.worker?worker")
      .then((editorWorker) => ({
        editor: (editorWorker as WorkerFactory).default,
      }));
  }
  return workerFactoriesPromise;
}

async function ensureMonacoEnvironment() {
  if (configured || typeof window === "undefined") {
    return;
  }

  const workerFactories = await loadWorkerFactories();
  window.MonacoEnvironment = {
    getWorker(_workerId, label) {
      const WorkerCtor = workerFactories[workerModuleKeyForLabel(label)];
      return new WorkerCtor();
    },
  };
  configured = true;
}

export async function loadMonaco() {
  if (!monacoPromise) {
    monacoPromise = Promise.all([
      import("monaco-editor/min/vs/editor/editor.main.css"),
      import("monaco-editor/esm/vs/editor/editor.api.js"),
      ensureMonacoEnvironment(),
    ]).then(([, monaco]) => monaco as unknown as MonacoModule);
  }
  return monacoPromise;
}
