# Retained session images

`Read` finds inline images retained in native Claude Code and Codex
transcripts. The caller supplies an already linked, mapped `sessions.Session`;
this package does not decide which sessions belong to a pull request.

Candidates carry decoded bytes, their recorded media type and graph references
to the owning input or tool call. The caller must sniff the bytes before using
them as review attachments. Human inputs are eligible only when the mapped
input identifies a human origin. Tool results keep their tool-call origin;
an injected user-role image is never assumed to be a human attachment. Claude
direct pasted images additionally require an ordinary non-meta envelope, with
absent or explicit human origin and absent or typed prompt source. Peer, system,
unknown-origin and harness text-wrapper records are excluded even if the mapper
classified accompanying text as human.

Claude content supports pasted `image` blocks and images nested under a
`tool_result`. Codex supports pasted `input_image` data URLs associated with a
mapped human turn, and structured image content in function/custom tool call
outputs. Nested content containers and JSON-encoded structured outputs are
read. Images referenced only by a URL or filesystem path are unavailable: no
network request or referenced image-file read is made.

Reachable child sessions are read from the native transcript files their graph
source references name. Claude uses its session's `subagents` directory. Codex
uses its native `sessions` directory, with a bounded directory walk to resolve
the recorded transcript basenames. An opened directory confines symlinks.
Unreachable child transcripts, Entire checkpoint payloads, unsupported vendors
and review-generation sessions supply no images.

Defaults allow 20 images, 10 MiB per image and 64 MiB decoded bytes per read.
Independent bounds limit a record to 16 MiB, transcript scanning to 256 MiB,
directory discovery to 100,000 entries and diagnostics to 64 entries. Callers
can lower image, byte and scan limits. Unsupported or unavailable content and
limits produce nonfatal diagnostics without transcript text, paths or image
payloads. Context cancellation stops discovery and scanning.

Run `just oss-go test ./sessions/sessionimages` from the monorepo root. The
tests use synthetic payloads, real native-mapper references, subagent origins,
source filtering, budgets and symlink confinement.
