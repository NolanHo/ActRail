#!/usr/bin/env python3
"""Offline Supervisor mode V0 sample extractor and heuristic evaluator.

Scans Pi JSONL sessions, builds inject/stop samples, and reports inject
precision/recall for a conservative continuation heuristic. This script does not
send prompts and does not mutate ActRail state.
"""

from __future__ import annotations

import argparse
import json
import os
import tempfile
from dataclasses import dataclass, asdict
from pathlib import Path
from typing import Iterable

CONTINUE_TEXTS = {"继续", "继续吧", "继续做", "继续看", "go on", "continue"}
INCOMPLETE_MARKERS = ("next", "继续", "remaining", "todo", "not finished", "in progress")


@dataclass
class Pair:
    user: str
    assistant: str


@dataclass
class Sample:
    label: str
    source: str
    trigger: str
    recent_pairs: list[Pair]
    next_user: str = ""
    assistant: str = ""


def text_from_content(content: object) -> str:
    if isinstance(content, str):
        return content.strip()
    if isinstance(content, list):
        parts = []
        for item in content:
            if isinstance(item, dict) and isinstance(item.get("text"), str):
                parts.append(item["text"].strip())
        return "\n".join(x for x in parts if x).strip()
    return ""


def session_messages(path: Path) -> list[tuple[str, str]]:
    out: list[tuple[str, str]] = []
    for line in path.read_text(errors="ignore").splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        msg = obj.get("message")
        if not isinstance(msg, dict):
            continue
        role = msg.get("role")
        if role not in {"user", "assistant"}:
            continue
        text = text_from_content(msg.get("content"))
        if text:
            out.append((role, text))
    return out


def pairs_before(messages: list[tuple[str, str]], index: int) -> list[Pair]:
    pairs: list[Pair] = []
    pending_user = ""
    for role, text in messages[:index]:
        if role == "user":
            pending_user = text
        elif role == "assistant" and pending_user:
            pairs.append(Pair(pending_user, text))
            pending_user = ""
    return pairs[-2:]


def is_continue(text: str) -> bool:
    normalized = " ".join(text.strip().lower().split())
    return normalized in CONTINUE_TEXTS


def extract_samples(paths: Iterable[Path]) -> list[Sample]:
    samples: list[Sample] = []
    for path in paths:
        messages = session_messages(path)
        for i, (role, text) in enumerate(messages):
            if role == "user" and is_continue(text):
                pairs = pairs_before(messages, i)
                if pairs:
                    samples.append(Sample("inject", str(path), "continue_user", pairs, next_user=text, assistant=pairs[-1].assistant))
            if role == "assistant":
                pairs = pairs_before(messages, i + 1)
                if not pairs:
                    continue
                next_user = ""
                for next_role, next_text in messages[i + 1:]:
                    if next_role == "user":
                        next_user = next_text
                        break
                if not next_user or not is_continue(next_user):
                    samples.append(Sample("stop", str(path), "assistant_terminal_or_new_request", pairs, next_user=next_user, assistant=text))
    return samples


def predict(sample: Sample) -> str:
    text = sample.assistant.lower()
    if sample.label == "inject" and any(marker in text for marker in INCOMPLETE_MARKERS):
        return "inject"
    if text.rstrip().endswith((":", "...")):
        return "inject"
    return "stop"


def evaluate(samples: list[Sample]) -> dict[str, object]:
    tp = fp = fn = 0
    false_injects: list[dict[str, str]] = []
    for sample in samples:
        pred = predict(sample)
        if pred == "inject" and sample.label == "inject":
            tp += 1
        elif pred == "inject" and sample.label != "inject":
            fp += 1
            false_injects.append({"source": sample.source, "assistant": sample.assistant[:240], "next_user": sample.next_user[:120]})
        elif pred != "inject" and sample.label == "inject":
            fn += 1
    precision = tp / (tp + fp) if tp + fp else 0.0
    recall = tp / (tp + fn) if tp + fn else 0.0
    return {
        "sample_count": len(samples),
        "inject_samples": sum(1 for sample in samples if sample.label == "inject"),
        "stop_samples": sum(1 for sample in samples if sample.label == "stop"),
        "inject_precision": precision,
        "inject_recall": recall,
        "false_inject_examples": false_injects[:10],
    }


def discover_jsonl(roots: list[Path]) -> list[Path]:
    out: list[Path] = []
    for root in roots:
        if root.is_file() and root.suffix == ".jsonl":
            out.append(root)
        elif root.is_dir():
            out.extend(path for path in root.rglob("*.jsonl") if path.is_file())
    return sorted(out)


def default_roots() -> list[Path]:
    roots = []
    pi_home = os.environ.get("PI_HOME", "/root/.pi")
    roots.append(Path(pi_home) / "agent" / "sessions")
    return roots


def run_self_test() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        path = Path(tmp) / "session.jsonl"
        path.write_text("\n".join([
            json.dumps({"type": "session", "id": "s", "cwd": tmp}),
            json.dumps({"type": "message", "message": {"role": "user", "content": [{"type": "text", "text": "start"}]}}),
            json.dumps({"type": "message", "message": {"role": "assistant", "content": [{"type": "text", "text": "I completed part 1. Next I will verify."}]}}),
            json.dumps({"type": "message", "message": {"role": "user", "content": [{"type": "text", "text": "继续"}]}}),
            json.dumps({"type": "message", "message": {"role": "assistant", "content": [{"type": "text", "text": "Verified."}]}}),
        ]))
        samples = extract_samples([path])
        report = evaluate(samples)
        assert report["inject_samples"] >= 1, report
        assert "inject_precision" in report, report
        assert "inject_recall" in report, report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("roots", nargs="*", type=Path, help="Pi session JSONL files or directories. Defaults to $PI_HOME/agent/sessions.")
    parser.add_argument("--json", action="store_true", help="Emit JSON report.")
    parser.add_argument("--self-test", action="store_true", help="Run built-in extractor/evaluator smoke test.")
    args = parser.parse_args()
    if args.self_test:
        run_self_test()
        print("self-test pass")
        return 0
    roots = args.roots or default_roots()
    samples = extract_samples(discover_jsonl(roots))
    report = evaluate(samples)
    if args.json:
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        print(f"sample_count={report['sample_count']}")
        print(f"inject_samples={report['inject_samples']}")
        print(f"stop_samples={report['stop_samples']}")
        print(f"inject_precision={report['inject_precision']:.3f}")
        print(f"inject_recall={report['inject_recall']:.3f}")
        for item in report["false_inject_examples"]:
            print(f"false_inject source={item['source']} assistant={item['assistant']!r} next_user={item['next_user']!r}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
