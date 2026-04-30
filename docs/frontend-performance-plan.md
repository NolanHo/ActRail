# Frontend editing performance plan

## Problem

Composer editing still feels slow. The current evidence points to runtime main-thread work, not the build tool:

- INP observed by user: about 305 ms.
- Build tool changes would not reduce browser-side input latency unless they also change shipped code.
- Prior bundle inspection found large deferred chunks from Monaco and token counting; `js-tiktoken` was removed from the interaction path in `db05c0e`.

## Constraints

- Use worktrees and pull requests. Avoid large mixed patches on `main`.
- Keep each PR independently deployable.
- Prefer changes with measurable or inspectable impact.
- Do not remove existing features to make latency disappear.
- Keep session editing, conversation scrolling, file viewer, workspace, and mobile behavior intact.

## Measurement loop

Use this loop for each PR:

1. Run frontend tests for touched areas.
2. Run `npm --prefix web run build` and record emitted chunk sizes.
3. In browser DevTools Performance, record typing 5 characters into composer on a long conversation.
4. Compare:
   - INP or longest interaction task
   - scripting time during typing
   - number of rendered message rows
   - main bundle and lazy bundle sizes

## PR sequence

### PR 1: document and split heavy UI from editing path

Goal: keep workspace/file-viewer code out of the initial editing path.

Implementation candidates:

- Lazy-load workspace overlays and file viewer components.
- Keep toolbar buttons synchronous, but load heavy panels only after click.
- Add loading skeletons for lazy panels.

Validation:

- AppShell tests still pass.
- File viewer and workspace tests still pass.
- Initial bundle should not include workspace-heavy code.

Risk:

- Dialog open timing changes.
- Tests that mock workspace components may need async waits.

### PR 2: store selectors for composer and conversation

Goal: input draft updates must not re-render session rail, conversation timeline, or workspace.

Implementation candidates:

- Add selector-based store hooks around `useSyncExternalStore`.
- Replace broad `useSessionsStore`, `useMessagesStore`, `useComposerStore`, and `useLiveSessionStore` usage in hot components.
- Start with Composer and ConversationPane.

Validation:

- Add render-count test or development-only probe if feasible.
- Composer tests and ConversationPane tests pass.

Risk:

- Selector equality bugs can hide updates.
- Store state object mutation would break selectors; verify stores replace state immutably.

### PR 3: message row memoization and markdown cache

Goal: new input or live state changes should not re-render historical message bodies.

Implementation candidates:

- Extract `MessageRow` component and wrap with `memo`.
- Pass stable row props from memoized row derivation.
- Cache markdown render input by stable message key plus text length and streaming flag.
- Throttle streaming assistant render updates to 100-150 ms.

Validation:

- Long conversation scroll behavior unchanged.
- Streaming assistant updates still visible.
- Markdown link click handlers still open files.

Risk:

- Stale markdown if cache key misses fields.
- Streaming throttling can make output feel less live if interval too high.

### PR 4: lightweight message list virtualization

Goal: large transcripts should not keep hundreds of heavy DOM nodes active.

Implementation candidates:

- Window rows around viewport with top/bottom spacers.
- Keep day separators and history controls in the row model.
- Preserve scroll-to-bottom, jump-to-previous-user, and load-older anchoring.

Validation:

- Long transcript manual test.
- Jump previous user and load older tests.

Risk:

- Highest risk item. Scroll anchoring regressions are likely.
- Should wait until PR 2 and PR 3 land.

## Current non-goals

- Switching to Bun for frontend compilation. Bun may reduce install/build time, but it does not directly reduce browser INP.
- Replacing Monaco. Current plan only defers and trims Monaco loading.
- Removing markdown, code blocks, or file viewer functionality.

## Open questions

- Which user flow produced INP 305 ms: composer typing, settings editing, session switching, or file viewer editing?
- Browser/device profile for the measurement.
- Transcript size when latency was observed.
