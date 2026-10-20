# traceupload

Builds the redacted session archive `baum upload` and `baum setup --upload`
send to the hosted server the user logged in to. The server reads exactly one
`ManifestVersion` and rejects any other; servers and CLIs of different
releases meet, so changing it is an API change (see "Compatibility" in the
[baumcli README](../baumcli/README.md)).

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
- `MaxUnpackedBytes` (512 MiB) bounds the decompressed tar stream: manifest,
  members and tar headers. The builder measures that stream
  (`Bundle.UnpackedSize`) as it writes it, and the CLI refuses an archive over
  the bound before uploading it, because the server's worker would refuse to
  unpack it.
