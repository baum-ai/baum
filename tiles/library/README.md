# Tile library

baum's built-in custom tile types, which a review generation offers to the
analysis and fills for the pull requests they apply to: `schema_change` and
`api_change`. See "Tile library" in [ARCHITECTURE.md](../../ARCHITECTURE.md).

Each type is a directory in the layout [the guide](../kit/GUIDE.md)
describes. Changing a type changes its digest: update `digests` in
`library_test.go` in the same commit and say what changed. Samples are
synthetic, in a fake repository; never put real code in them.

Go tests: `go test ./tiles/library/`. Pictures of every sample:
`just local_app::tile-type-screens`.
