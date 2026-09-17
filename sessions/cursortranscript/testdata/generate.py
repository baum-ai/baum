#!/usr/bin/env python3
"""Regenerate the committed Cursor transcript fixture."""

import json
import os
import sqlite3


HERE = os.path.dirname(os.path.abspath(__file__))
PATH = os.path.join(HERE, "store.vscdb")
BASE = 1767225600000


def put(conn, key, value):
    conn.execute("INSERT INTO cursorDiskKV VALUES (?, ?)", (key, None if value is None else json.dumps(value)))


def header(conn, composer_id, name, created, subagent=False, workspace=True):
    value = {"name": name}
    if workspace:
        value["workspaceIdentifier"] = {
            "id": "workspace-1",
            "uri": {"scheme": "file", "fsPath": "/repo"},
        }
    conn.execute(
        "INSERT INTO composerHeaders VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (composer_id, "workspace-1", created, created + 60000, 0, int(subagent), created + 60000, 0, json.dumps(value)),
    )


def tool(name, status, call_id, params=None, result=None, model_call="", **extra):
    record = {
        "tool": 38,
        "name": name,
        "status": status,
        "toolCallId": call_id,
        "modelCallId": model_call,
        "toolIndex": 1,
        "params": json.dumps(params or {}),
        "rawArgs": json.dumps(params or {}),
    }
    if result is not None:
        record["result"] = json.dumps(result)
    record.update(extra)
    return record


def add_composer(conn, composer_id, *, bubbles, name, created=BASE, status="completed", children=None, header_row=True, subagent=False, **extra):
    if header_row:
        header(conn, composer_id, name, created, subagent=subagent)
    data = {
        "_v": 17,
        "composerId": composer_id,
        "name": name,
        "status": status,
        "createdAt": created,
        "lastUpdatedAt": created + 60000,
        "unifiedMode": "agent",
        "agentBackend": "cursor-agent",
        "modelConfig": {"modelName": "default-model", "thinkingLevel": "medium"},
        "workspaceIdentifier": {"id": "workspace-1", "uri": {"scheme": "file", "fsPath": "/repo"}},
        "contextTokensUsed": 1234,
        "contextTokenLimit": 100000,
        "promptTokenBreakdown": {"totalUsedTokens": 1234, "maxTokens": 100000},
        "fullConversationHeadersOnly": [{"bubbleId": bubble_id, "type": body["type"]} for bubble_id, body in bubbles],
        "subagentComposerIds": children or [],
    }
    data.update(extra)
    put(conn, f"composerData:{composer_id}", data)
    for bubble_id, body in bubbles:
        body = {"_v": 3, "bubbleId": bubble_id, "createdAt": "2026-01-01T00:00:00Z", "text": "", "tokenCount": {"inputTokens": 0, "outputTokens": 0}, **body}
        put(conn, f"bubbleId:{composer_id}:{bubble_id}", body)


def main():
    for suffix in ("", "-wal", "-shm"):
        try:
            os.remove(PATH + suffix)
        except FileNotFoundError:
            pass
    conn = sqlite3.connect(PATH)
    conn.execute("CREATE TABLE composerHeaders (composerId TEXT PRIMARY KEY, workspaceId TEXT, createdAt INTEGER, lastUpdatedAt INTEGER, isArchived INTEGER, isSubagent INTEGER, recency INTEGER, checkpointAt INTEGER, value TEXT)")
    conn.execute("CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)")

    root_bubbles = [
        ("u1", {"type": 1, "text": "Implement it", "richText": {"root": {}}, "capabilityType": 1, "createdAt": "2026-01-01T00:00:03Z"}),
        ("think", {"type": 2, "thinking": {"text": "private", "signature": "opaque"}, "createdAt": "2026-01-01T00:00:02Z"}),
        ("read", {"type": 2, "modelInfo": {"modelName": "override-model"}, "tokenCount": {"inputTokens": 100, "outputTokens": 20}, "toolFormerData": tool("read_file_v2", "completed", "call-read", {"targetFile": "/repo/main.go"}, {"contents": "package main"}, "model-a")}),
        ("mixed", {"type": 2, "text": "The edit failed.", "toolFormerData": tool("edit_file_v2", "error", "call-edit", {"relativeWorkspacePath": "pkg/edit.go"}, result={"partial": True}, model_call="model-a", error={"message": "conflict"})}),
        ("after-tool", {"type": 2, "text": "Trying another approach."}),
        ("cancel", {"type": 2, "toolFormerData": tool("run_terminal_command_v2", "cancelled", "call-cancel", {"command": "sleep 10"}, result="stopped", userDecision="rejected")}),
        ("loading", {"type": 2, "toolFormerData": tool("web_search", "loading", "call-loading", {"query": "mapper"})}),
        ("degenerate", {"type": 2, "toolFormerData": {"tool": 0, "name": None}}),
        ("u2", {"type": 1, "text": "Delegate the rest"}),
        ("spawn-child", {"type": 2, "toolFormerData": tool("task_v2", "completed", "call-child", {"description": "child task"}, result={"agentId": "child"}, additionalData={"status": "completed", "terminationReason": "done", "subagentComposerId": "child"})}),
        ("spawn-dangling", {"type": 2, "toolFormerData": tool("task_v2", "error", "call-dangling", {"description": "missing"}, additionalData={"status": "error", "subagentComposerId": "dangling"})}),
        ("phantom", {"type": 2, "toolFormerData": tool("task_v2", "completed", "call-phantom", {"description": "phantom"}, additionalData={"status": "completed", "subagentComposerId": "phantom"})}),
        ("empty", {"type": 2}),
    ]
    add_composer(
        conn,
        "root",
        bubbles=root_bubbles,
        name="Cursor fixture",
        status="aborted",
        children=["child", "dangling"],
        createdOnBranch="feature/cursor",
        committedToBranch="feature/cursor",
        codeBlockData={
            "file:///repo/pkg/edit.go": {
                "33333333-3333-4333-8333-333333333333": {
                    "bubbleId": "mixed",
                    "codeblockId": "33333333-3333-4333-8333-333333333333",
                    "diffId": "11111111-1111-4111-8111-111111111111",
                    "status": "accepted",
                    "uri": {"scheme": "file", "fsPath": "/repo/pkg/edit.go"},
                }
            },
            "file:///repo/pkg/orphan.go": {
                "44444444-4444-4444-8444-444444444444": {
                    "bubbleId": "after-tool",
                    "codeblockId": "44444444-4444-4444-8444-444444444444",
                    "diffId": "55555555-5555-4555-8555-555555555555",
                    "status": "completed",
                    "uri": {"scheme": "file", "fsPath": "/repo/pkg/orphan.go"},
                }
            }
        },
    )
    put(
        conn,
        "codeBlockDiff:root:11111111-1111-4111-8111-111111111111",
        {
            "newModelDiffWrtV0": [
                {
                    "original": {"startLineNumber": 2, "endLineNumberExclusive": 3},
                    "modified": ["replacement"],
                }
            ],
            "originalModelDiffWrtV0": [
                {
                    "original": {"startLineNumber": 2, "endLineNumberExclusive": 3},
                    "modified": ["original"],
                }
            ],
        },
    )
    put(
        conn,
        "codeBlockDiff:root:55555555-5555-4555-8555-555555555555",
        {
            "newModelDiffWrtV0": [
                {
                    "original": {"startLineNumber": 7, "endLineNumberExclusive": 7},
                    "modified": ["inserted", "lines"],
                }
            ],
            "originalModelDiffWrtV0": [],
        },
    )
    conn.execute(
        "INSERT INTO cursorDiskKV VALUES (?, ?)",
        (
            "codeBlockDiff:root:22222222-2222-4222-8222-222222222222",
            '{"newModelDiffWrtV0":[{"original":{"startLineNumber":1,"endLineNumberExclusive":2},"modified":[null]}],"originalModelDiffWrtV0":[]}',
        ),
    )
    put(conn, "bubbleId:root:unlisted", {"_v": 3, "bubbleId": "unlisted", "type": 2, "text": "retry copy", "createdAt": "2026-01-01T00:00:04Z"})

    child_bubbles = [
        ("cu", {"type": 1, "text": "Child task", "subagentSpawnTaskToolCallId": "call-child"}),
        ("ct", {"type": 2, "toolFormerData": tool("task_v2", "completed", "call-grand", {"description": "grandchild"}, result={"agentId": "grandchild"}, additionalData={"status": "completed", "subagentComposerId": "grandchild"})}),
    ]
    add_composer(conn, "child", bubbles=child_bubbles, name="Child", created=BASE + 1000, children=["grandchild"], subagent=True)

    grand_bubbles = [
        ("gu", {"type": 1, "text": "Grandchild task", "subagentSpawnTaskToolCallId": "call-grand"}),
        ("ga", {"type": 2, "text": "Done"}),
    ]
    add_composer(conn, "grandchild", bubbles=grand_bubbles, name="Grandchild", created=BASE + 2000, subagent=True)

    add_composer(conn, "headerless", bubbles=[("hu", {"type": 1, "text": "Headerless"})], name="Headerless", created=BASE + 3000, header_row=False)
    add_composer(conn, "draft", bubbles=[], name="Draft", created=BASE + 4000)
    header(conn, "data-less", "Data-less", BASE + 5000)
    put(conn, "bubbleId:data-less:orphan", {"_v": 3, "bubbleId": "orphan", "type": 2, "text": "Unordered evidence only", "createdAt": "2026-01-01T00:00:05Z"})

    put(conn, "composerData:null", None)
    conn.execute("INSERT INTO cursorDiskKV VALUES (NULL, NULL)")
    put(conn, "messageRequestContext:root:u1", {"git": "context"})
    put(conn, "agentKv:blob", {"opaque": True})
    conn.commit()
    conn.close()


if __name__ == "__main__":
    main()
