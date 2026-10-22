// Package uibuild lets tests of an embedded app skip when it is not built.
// Only a release commit carries the built viewer and local app; any other
// checkout embeds an empty directory until "just viewer-build" runs. Only tests
// import this package.
package uibuild

import (
	"io/fs"
	"os"
	"testing"
)

// Require skips a test that reads the built app when there is none, unless
// BAUM_REQUIRE_UI is set: a check that built the app first sets it, so a
// missing build fails there instead of passing silently.
func Require(t testing.TB, assets fs.FS) {
	t.Helper()
	if _, err := fs.Stat(assets, "index.html"); err == nil {
		return
	}
	if os.Getenv("BAUM_REQUIRE_UI") != "" {
		t.Fatal("the app is not built, and BAUM_REQUIRE_UI is set; run \"just viewer-build\" first")
	}
	t.Skip("the app is not built; run \"just viewer-build\" to test it")
}
