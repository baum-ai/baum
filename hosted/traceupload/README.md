# traceupload

Builds the redacted session archive the CLI uploads. The server side (upload
endpoint, unpacking and the processing worker) is `internal/traceingest`. The
two share `ManifestVersion` and deploy together, so there is one format and
the worker rejects any other.

## Archive contract

- Redaction runs locally, before anything is archived, with the goredact
  settings in `redactionConfig`. It masks detected secrets and does not
  guarantee that all sensitive content is gone. Repository and worktree roots
  are sent verbatim, because attribution needs them.
- Each session has exactly one root transcript and unique non-empty file names.
  NUL bytes fail the bundle; binary content is not supported. Member paths are
  opaque and hide the local directory layout. Staging uses private files.
- The first regular tar member is `manifest.json`, at most `MaxManifestBytes`.
  It lists every file with its redacted size and SHA-256 and records the
  redaction configuration and finding counts.
- The server accepts only confined relative paths, each listed once with the
  declared size and digest. Directories, links and devices are ignored and
  never satisfy a listed file; any other entry that is not a regular file
  carries data the manifest does not check, so it is rejected.

## Server rules

- Upload needs write access to the target repository, and the repository
  turned on in baum: a disabled one answers 409 "repository is not enabled in
  baum" (`AuthorizeRepositoryUpload`; see
  [githubintegration](../githubintegration/README.md), "Enabled switch"), as
  `baum setup` uploads nothing for it. Reads are unaffected.
  `traceingest.MaxArchiveBytes` bounds the compressed upload at
  `MaxUnpackedBytes` plus 1/64: deflate grows incompressible input by only a
  few bytes per 64 KiB block, so no archive the worker would accept is larger.
- `MaxUnpackedBytes` (512 MiB) bounds the decompressed tar stream: manifest,
  members and tar headers. The worker holds every member in memory, so the
  manifest's listed sizes are checked against it before any member is read,
  and every byte the worker decompresses, skipped entries included, counts
  toward it. An archive over it fails its job at once instead of retrying.
  The worker checks for cancellation between members. The builder measures
  the same stream (`Bundle.UnpackedSize`) and the CLI refuses an archive over
  the bound before uploading it ([baumcli](../baumcli/README.md)). 512 MiB is
  about twice the largest real history measured (238 MiB) and half the old
  1 GiB bound, which let a 1 MiB upload expand to a gigabyte in a worker
  shared with review generation and sync.
- `StaleUploadAge` is derived from `MaxUploadDuration` so an upload can never
  still be streaming when reconciliation deletes its object.
- An upload whose repository was turned off, archived or disconnected (or
  whose installation was disconnected) after it was accepted completes storing
  nothing.
- One bad session (unknown vendor, mapper refusal, invalid graph, stale
  snapshot, refused join, a session another tree holds and the arrival may not
  take over) is a counted skip and the rest still store. A tree that holds a
  session stored as a standalone tree of its own, such as a Codex child the
  Entire compact fallback stored first, takes that session over
  ([sessionstore](../sessionstore/README.md), "Taking over a standalone
  tree"); the stored-tree log line names the trees it took over. Zero
  stored trees is a valid completion. Object, archive and database failures
  fail the job.
- After native uploads replace trees, the Entire envelope is restored by the
  repository's entire-ingest job, not by the upload: the worker invalidates the
  replaced roots' completed checkpoints and requests a re-ingest in one
  transaction (best-effort; a failure is logged), and exports nothing inline.
  The re-ingest tier applies the checkpoints later
  ([entireingest](../entireingest/README.md), "Coexistence with uploads").
