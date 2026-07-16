#!/usr/bin/env python3
"""Strip issues/prompt-optimization APIs from MLflow service.proto for the Go SDK."""

from __future__ import annotations

import re
import sys

RPC_NAMES = (
    "createIssue|updateIssue|getIssue|searchIssues|"
    "createPromptOptimizationJob|getPromptOptimizationJob|"
    "searchPromptOptimizationJobs|cancelPromptOptimizationJob|"
    "deletePromptOptimizationJob"
)

MESSAGE_NAMES = [
    "CreatePromptOptimizationJob",
    "GetPromptOptimizationJob",
    "SearchPromptOptimizationJobs",
    "CancelPromptOptimizationJob",
    "DeletePromptOptimizationJob",
    "PromptOptimizationJob",
    "PromptOptimizationJobConfig",
    "PromptOptimizationJobTag",
]

FORBIDDEN = [
    'import "issues.proto"',
    'import "prompt_optimization.proto"',
    "PromptOptimizationJob",
    "CreateIssue",
    "createPromptOptimizationJob",
    "Issue RPCs",
    "Prompt Optimization API Messages",
    "prompt optimization job",
]


def remove_rpc_blocks(text: str) -> str:
    """Remove target RPCs and any immediately preceding comment/blank lines."""
    # End at the RPC's own closing brace so following kept-RPC comments survive.
    pattern = re.compile(
        rf"  rpc (?:{RPC_NAMES})\b.*?\n  \}}\n?",
        re.DOTALL,
    )
    while True:
        match = pattern.search(text)
        if not match:
            break
        start = match.start()
        lines = text[:start].splitlines(keepends=True)
        i = len(lines)
        while i > 0:
            stripped = lines[i - 1].strip()
            if stripped == "" or stripped.startswith("//"):
                i -= 1
                continue
            break
        text = "".join(lines[:i]) + text[match.end() :]
    return text


def remove_messages(text: str) -> str:
    """Remove target messages without consuming following section headers."""
    for name in MESSAGE_NAMES:
        # Also drop a section header immediately above the first stripped message.
        pattern = re.compile(
            rf"(?:^//[^\n]*\n)*message {name} \{{.*?(?=\nmessage |\n// =+|\Z)",
            re.DOTALL | re.MULTILINE,
        )
        text = pattern.sub("", text)
    return text


def cleanup_orphans(text: str) -> str:
    """Remove any leftover headers and collapse blank lines from removals."""
    for orphan in [
        r"\n  // Issue RPCs\n+",
        r"\n// ========== Prompt Optimization API Messages ==========+\n+",
    ]:
        text = re.sub(orphan, "\n", text)
    return re.sub(r"\n{3,}", "\n\n", text)


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <service.proto>", file=sys.stderr)
        return 2

    path = sys.argv[1]
    with open(path) as f:
        content = f.read()

    content = re.sub(r'^import "issues\.proto";\n', "", content, flags=re.MULTILINE)
    content = re.sub(r'^import "prompt_optimization\.proto";\n', "", content, flags=re.MULTILINE)
    content = remove_rpc_blocks(content)
    content = remove_messages(content)
    content = cleanup_orphans(content)

    for token in FORBIDDEN:
        if token in content:
            print(
                f"ERROR: service.proto still contains {token!r} after post-processing",
                file=sys.stderr,
            )
            return 1

    with open(path, "w") as f:
        f.write(content)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
