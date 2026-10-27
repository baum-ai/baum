package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplaceHome(t *testing.T) {
	const home = "/home/user"
	users := "Us" + "ers"
	q := func(parts ...string) string { return `"` + strings.Join(parts, `" "`) + `"` }
	for _, c := range []struct{ home, in, want string }{
		{home, q(home+"/src", home, home+"name", home+".bak", "x"+home+" y", "cd "+home+"/x", "a="+home, "file://"+home+"/x"),
			q("~/src", "~", home+"name", home+".bak", "x"+home+" y", "cd ~/x", "a=~", "file://~/x")},
		// Not inside a longer path.
		{home, q("/data" + home + "/src"), q("/data" + home + "/src")},
		// A trailing separator on the home is dropped.
		{home + "/", q(home+"/src", home), q("~/src", "~")},
		// After an escaped newline or tab in the JSON text.
		{home, `"a\n` + home + `/x\t` + home + `"`, `"a\n~/x\t~"`},
		// Windows: escaped backslashes, forward slashes, any case.
		// (The directory name is split so no source line spells a real home.)
		{`C:\` + users + `\ada`, q(`C:\\`+users+`\\ada\\src`, "C:/"+users+"/ada/src", `c:\\`+strings.ToLower(users)+`\\ADA\\src`),
			q(`~\\src`, "~/src", `~\\src`)},
		{"", q(home), q(home)},
		{"/", q(home), q(home)},
	} {
		if got := string(replaceHome([]byte(c.in), homeForms(c.home))); got != c.want {
			t.Errorf("replaceHome(%s, %q)\n got %s\nwant %s", c.in, c.home, got, c.want)
		}
	}
}

func TestReplaceHomeFollowsTheHomesLink(t *testing.T) {
	// Resolved, so the only link the home's resolution follows is the test's
	// own: the temporary directory sits behind one on macOS (/var is
	// /private/var) and under a short name on Windows (RUNNER~1).
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symbolic links here:", err)
	}
	in := `"` + link + `/a" "` + target + `/b"`
	if got := string(replaceHome([]byte(in), homeForms(link))); got != `"~/a" "~/b"` {
		t.Errorf("got %s", got)
	}
}
