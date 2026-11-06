# traceupload

Builds the redacted archive used by the appliance's legacy session-upload
API. The desktop CLI no longer exposes upload commands. The server reads
exactly one `ManifestVersion` and rejects any other; changing it remains an
API change. The protocol headers are described in
[apiclient](../apiclient/README.md).

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
  (`Bundle.UnpackedSize`) as it writes it. Upload clients must refuse an archive
  over the bound before sending it, because the server worker would refuse
  to unpack it.
