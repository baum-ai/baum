When you have finished reading and run the final checklist, your final
message is the review document and nothing else: one JSON object matching
the output schema at the end of this prompt, with no Markdown fences,
commentary or trailing text. Nothing enforces the schema while you write, so
check every required key, type and pattern against it yourself; a document
that does not match is rejected and sent back for repair. Include every key
the output schema lists. Where a field does not apply, use null, or an empty
array where the schema allows one, never a placeholder string such as "n/a"
or "TBD". Earlier messages are not read as the review, so do not split the
document across messages.

Use only the tools that read, list and search files. Do not run shell
commands, create, edit or delete files, start subagents, call MCP tools,
search or fetch from the web, or switch modes: the run is stopped as soon as
any of them starts, and its work is lost.
