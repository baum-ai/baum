// Generated from the diff of acme/demo#7 (a phone transport for
// the daemon): the hunks the review view's fixture cites, as the index stores them.
import type { IndexedHunk } from "@baum/viewer/review-document";

export const HUNKS: IndexedHunk[] = [
  {
    id: "h_routes",
    path: "oss/internal/uiserver/remote.go",
    change: "added",
    role: "code",
    new_range: [17, 49],
    additions: 49,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_routes",
    diff: {
      header: "@@ -0,0 +17,49 @@",
      lines: [
        {
          kind: "add",
          text: "// remoteRoutes is what a paired phone may ask of the daemon, and nothing else",
          new_line: 17,
        },
        {
          kind: "add",
          text: "// (see RemoteHandler). When you add an API route, phones do not get it: only",
          new_line: 18,
        },
        {
          kind: "add",
          text: "// what is listed here is served remotely, and each entry must be a read, or a",
          new_line: 19,
        },
        {
          kind: "add",
          text: "// write the phone is meant to make. Check that its handler returns nothing a",
          new_line: 20,
        },
        {
          kind: "add",
          text: "// phone should not hold (credentials, tokens, paths) and that the pattern",
          new_line: 21,
        },
        {
          kind: "add",
          text: "// cannot match a more powerful sibling route. TestRemoteRoutesCoverLocalAPI",
          new_line: 22,
        },
        {
          kind: "add",
          text: "// fails until a new route is either listed here or in remoteRefused.",
          new_line: 23,
        },
        {
          kind: "add",
          text: "//",
          new_line: 24,
        },
        {
          kind: "add",
          text: "// A GET entry also admits HEAD, except /api/events.",
          new_line: 25,
        },
        {
          kind: "add",
          text: "var remoteRoutes = []remoteRoute{",
          new_line: 26,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/health"},',
          new_line: 27,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/events"},',
          new_line: 28,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/index"},',
          new_line: 29,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/inbox"},',
          new_line: 30,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/prs"},',
          new_line: 31,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/versions"},',
          new_line: 32,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/document"},',
          new_line: 33,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/sessions"},',
          new_line: 34,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/sessions/{id}"},',
          new_line: 35,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/patch"},',
          new_line: 36,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/commits"},',
          new_line: 37,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/commit"},',
          new_line: 38,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/commit-patch"},',
          new_line: 39,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/file"},',
          new_line: 40,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/attachments/{id}"},',
          new_line: 41,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/facts"},',
          new_line: 42,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/facts/sessions"},',
          new_line: 43,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/facts/sessions/{id}"},',
          new_line: 44,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/facts/patch"},',
          new_line: 45,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/facts/commits"},',
          new_line: 46,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/facts/commit"},',
          new_line: 47,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/facts/commit-patch"},',
          new_line: 48,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/prs/review-threads"},',
          new_line: 49,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/prs/failing-checks"},',
          new_line: 50,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/prs/checks"},',
          new_line: 51,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/prs/check-log"},',
          new_line: 52,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/prs/description"},',
          new_line: 53,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/prs/conflicts"},',
          new_line: 54,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/prs/chat"},',
          new_line: 55,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/jobs"},',
          new_line: 56,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/jobs/{id}"},',
          new_line: 57,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/live-sessions"},',
          new_line: 58,
        },
        {
          kind: "add",
          text: '\t{"GET", "/api/agent-requests"},',
          new_line: 59,
        },
        {
          kind: "add",
          text: "\t// The API checks the review's event itself: APPROVE, COMMENT or REQUEST_CHANGES.",
          new_line: 60,
        },
        {
          kind: "add",
          text: '\t{"POST", "/api/prs/review"},',
          new_line: 61,
        },
        {
          kind: "add",
          text: '\t{"POST", "/api/prs/resolve-thread"},',
          new_line: 62,
        },
        {
          kind: "add",
          text: '\t{"POST", "/api/prs/refresh"},',
          new_line: 63,
        },
        {
          kind: "add",
          text: '\t{"POST", "/api/agent-requests/{id}/answer"},',
          new_line: 64,
        },
        {
          kind: "add",
          text: "}",
          new_line: 65,
        },
      ],
    },
  },
  {
    id: "h_refusals",
    path: "oss/internal/uiserver/remote.go",
    change: "added",
    role: "code",
    new_range: [91, 20],
    additions: 20,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_refusals",
    diff: {
      header: "@@ -0,0 +91,20 @@",
      lines: [
        {
          kind: "add",
          text: "// remotePathSegments splits a request path into segments when it is a plain",
          new_line: 91,
        },
        {
          kind: "add",
          text: '// absolute path that no router could read differently: no empty, "." or ".."',
          new_line: 92,
        },
        {
          kind: "add",
          text: "// segment, no trailing slash, no backslash or NUL, no encoded slash.",
          new_line: 93,
        },
        {
          kind: "add",
          text: "func remotePathSegments(r *http.Request) ([]string, bool) {",
          new_line: 94,
        },
        {
          kind: "add",
          text: "\tescaped := strings.ToLower(r.URL.EscapedPath())",
          new_line: 95,
        },
        {
          kind: "add",
          text: '\tif strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {',
          new_line: 96,
        },
        {
          kind: "add",
          text: "\t\treturn nil, false",
          new_line: 97,
        },
        {
          kind: "add",
          text: "\t}",
          new_line: 98,
        },
        {
          kind: "add",
          text: "\tpath := r.URL.Path",
          new_line: 99,
        },
        {
          kind: "add",
          text: '\tif !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\\\\\\x00") || strings.HasSuffix(path, "/") {',
          new_line: 100,
        },
        {
          kind: "add",
          text: "\t\treturn nil, false",
          new_line: 101,
        },
        {
          kind: "add",
          text: "\t}",
          new_line: 102,
        },
        {
          kind: "add",
          text: '\tsegments := strings.Split(path, "/")',
          new_line: 103,
        },
        {
          kind: "add",
          text: "\tfor _, s := range segments[1:] {",
          new_line: 104,
        },
        {
          kind: "add",
          text: '\t\tif s == "" || s == "." || s == ".." {',
          new_line: 105,
        },
        {
          kind: "add",
          text: "\t\t\treturn nil, false",
          new_line: 106,
        },
        {
          kind: "add",
          text: "\t\t}",
          new_line: 107,
        },
        {
          kind: "add",
          text: "\t}",
          new_line: 108,
        },
        {
          kind: "add",
          text: "\treturn segments, true",
          new_line: 109,
        },
        {
          kind: "add",
          text: "}",
          new_line: 110,
        },
      ],
    },
  },
  {
    id: "h_daemon",
    path: "oss/internal/cli/daemon.go",
    change: "modified",
    role: "code",
    new_range: [915, 7],
    additions: 1,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_daemon",
    diff: {
      header: "@@ -915,6 +915,7 @@",
      lines: [
        {
          kind: "context",
          text: "\t\t}",
          old_line: 915,
          new_line: 915,
        },
        {
          kind: "context",
          text: "\t\t// Everything the journaled jobs need is wired: start them.",
          old_line: 916,
          new_line: 916,
        },
        {
          kind: "context",
          text: "\t\tqueue.Resume()",
          old_line: 917,
          new_line: 917,
        },
        {
          kind: "add",
          text: "\t\tphones := openRemoteTransport(paths, database, server, log)",
          new_line: 918,
        },
        {
          kind: "context",
          text: "\t\treturn daemon.App{",
          old_line: 918,
          new_line: 919,
        },
        {
          kind: "context",
          text: "\t\t\tAPI:     api,",
          old_line: 919,
          new_line: 920,
        },
        {
          kind: "context",
          text: "\t\t\tAssets:  localapp.Handler(),",
          old_line: 920,
          new_line: 921,
        },
      ],
    },
  },
  {
    id: "h_shutdown",
    path: "oss/internal/cli/daemon.go",
    change: "modified",
    role: "code",
    new_range: [992, 17],
    additions: 7,
    deletions: 1,
    github_url: "https://github.com/acme/demo/pull/7/files#h_shutdown",
    diff: {
      header: "@@ -990,11 +992,17 @@",
      lines: [
        {
          kind: "context",
          text: "\t\t\t\t})",
          old_line: 990,
          new_line: 992,
        },
        {
          kind: "context",
          text: "\t\t\t\twg.Wait()",
          old_line: 991,
          new_line: 993,
        },
        {
          kind: "context",
          text: "\t\t\t},",
          old_line: 992,
          new_line: 994,
        },
        {
          kind: "remove",
          text: "\t\t\tShutdown:     queue.Shutdown,",
          old_line: 993,
        },
        {
          kind: "add",
          text: "\t\t\t// The phone connection stops first: its requests read the",
          new_line: 995,
        },
        {
          kind: "add",
          text: "\t\t\t// providers that close after.",
          new_line: 996,
        },
        {
          kind: "add",
          text: "\t\t\tShutdown: func(ctx context.Context) error {",
          new_line: 997,
        },
        {
          kind: "add",
          text: "\t\t\t\tphones.Close()",
          new_line: 998,
        },
        {
          kind: "add",
          text: "\t\t\t\treturn queue.Shutdown(ctx)",
          new_line: 999,
        },
        {
          kind: "add",
          text: "\t\t\t},",
          new_line: 1000,
        },
        {
          kind: "context",
          text: "\t\t\tDrain:        queue.Drain,",
          old_line: 994,
          new_line: 1001,
        },
        {
          kind: "context",
          text: "\t\t\tDrainRunning: queue.DrainRunning,",
          old_line: 995,
          new_line: 1002,
        },
        {
          kind: "context",
          text: "\t\t\tResume:       queue.Resume,",
          old_line: 996,
          new_line: 1003,
        },
        {
          kind: "context",
          text: "\t\t\tClose: func() {",
          old_line: 997,
          new_line: 1004,
        },
        {
          kind: "add",
          text: "\t\t\t\tphones.Close()",
          new_line: 1005,
        },
        {
          kind: "context",
          text: "\t\t\t\tsample.Close()",
          old_line: 998,
          new_line: 1006,
        },
        {
          kind: "context",
          text: "\t\t\t\tapi.Close()",
          old_line: 999,
          new_line: 1007,
        },
        {
          kind: "context",
          text: "\t\t\t\tgroupsAPI.Close()",
          old_line: 1000,
          new_line: 1008,
        },
      ],
    },
  },
  {
    id: "h_pairing",
    path: "oss/internal/remote/pairing.go",
    change: "added",
    role: "code",
    new_range: [21, 20],
    additions: 20,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_pairing",
    diff: {
      header: "@@ -0,0 +21,20 @@",
      lines: [
        {
          kind: "add",
          text: "const (",
          new_line: 21,
        },
        {
          kind: "add",
          text: "\t// OfferLifetime is how long a pairing offer can be redeemed.",
          new_line: 22,
        },
        {
          kind: "add",
          text: "\tOfferLifetime = 5 * time.Minute",
          new_line: 23,
        },
        {
          kind: "add",
          text: "\t// MaxOffers is how many offers can be live at once.",
          new_line: 24,
        },
        {
          kind: "add",
          text: "\tMaxOffers = 4",
          new_line: 25,
        },
        {
          kind: "add",
          text: "",
          new_line: 26,
        },
        {
          kind: "add",
          text: '\trelayTokenInfo = "baum-pair-relay-token"',
          new_line: 27,
        },
        {
          kind: "add",
          text: ")",
          new_line: 28,
        },
        {
          kind: "add",
          text: "",
          new_line: 29,
        },
        {
          kind: "add",
          text: "// Offer is what a pairing QR code carries. The secret lets one phone pair",
          new_line: 30,
        },
        {
          kind: "add",
          text: "// once, so an Offer must never be logged or stored. It has no exported secret",
          new_line: 31,
        },
        {
          kind: "add",
          text: "// field and prints without it under every fmt verb, as JSON and in slog; the",
          new_line: 32,
        },
        {
          kind: "add",
          text: "// link, which holds the secret, comes only from URI.",
          new_line: 33,
        },
        {
          kind: "add",
          text: "type Offer struct {",
          new_line: 34,
        },
        {
          kind: "add",
          text: "\tRelay   string",
          new_line: 35,
        },
        {
          kind: "add",
          text: "\tChannel string",
          new_line: 36,
        },
        {
          kind: "add",
          text: "\tKey     []byte",
          new_line: 37,
        },
        {
          kind: "add",
          text: "\tExpires time.Time",
          new_line: 38,
        },
        {
          kind: "add",
          text: "\tsecret  []byte",
          new_line: 39,
        },
        {
          kind: "add",
          text: "}",
          new_line: 40,
        },
      ],
    },
  },
  {
    id: "h_devices",
    path: "oss/internal/remote/devices.go",
    change: "added",
    role: "code",
    new_range: [21, 12],
    additions: 12,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_devices",
    diff: {
      header: "@@ -0,0 +21,12 @@",
      lines: [
        {
          kind: "add",
          text: "\t// MaxDevices is how many phones may be paired at once.",
          new_line: 21,
        },
        {
          kind: "add",
          text: "\tMaxDevices = 16",
          new_line: 22,
        },
        {
          kind: "add",
          text: "\t// TouchInterval is the least time between two persisted LastSeen updates",
          new_line: 23,
        },
        {
          kind: "add",
          text: "\t// of one device.",
          new_line: 24,
        },
        {
          kind: "add",
          text: "\tTouchInterval = 5 * time.Minute",
          new_line: 25,
        },
        {
          kind: "add",
          text: "",
          new_line: 26,
        },
        {
          kind: "add",
          text: "\tmaxNameRunes = 64",
          new_line: 27,
        },
        {
          kind: "add",
          text: '\tdefaultName  = "Phone"',
          new_line: 28,
        },
        {
          kind: "add",
          text: ")",
          new_line: 29,
        },
        {
          kind: "add",
          text: "",
          new_line: 30,
        },
        {
          kind: "add",
          text: "var (",
          new_line: 31,
        },
        {
          kind: "add",
          text: '\tErrTooManyDevices = errors.New("remote: at most 16 phones can be paired; revoke one first")',
          new_line: 32,
        },
      ],
    },
  },
  {
    id: "h_session",
    path: "oss/internal/remote/session.go",
    change: "added",
    role: "code",
    new_range: [140, 21],
    additions: 21,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_session",
    diff: {
      header: "@@ -0,0 +140,21 @@",
      lines: [
        {
          kind: "add",
          text: "func (r *Responder) Accept(msg1 []byte) (Accepted, error) {",
          new_line: 140,
        },
        {
          kind: "add",
          text: "\trnd := r.Random",
          new_line: 141,
        },
        {
          kind: "add",
          text: "\tif rnd == nil {",
          new_line: 142,
        },
        {
          kind: "add",
          text: "\t\trnd = rand.Reader",
          new_line: 143,
        },
        {
          kind: "add",
          text: "\t}",
          new_line: 144,
        },
        {
          kind: "add",
          text: "\tnow := time.Now()",
          new_line: 145,
        },
        {
          kind: "add",
          text: "\tif r.Now != nil {",
          new_line: 146,
        },
        {
          kind: "add",
          text: "\t\tnow = r.Now()",
          new_line: 147,
        },
        {
          kind: "add",
          text: "\t}",
          new_line: 148,
        },
        {
          kind: "add",
          text: "\t// ephemeral key, encrypted static key, payload and their tags",
          new_line: 149,
        },
        {
          kind: "add",
          text: "\tif len(msg1) > 32+48+MaxHandshakePayload+16 {",
          new_line: 150,
        },
        {
          kind: "add",
          text: "\t\treturn Accepted{}, ErrBadHandshake",
          new_line: 151,
        },
        {
          kind: "add",
          text: "\t}",
          new_line: 152,
        },
        {
          kind: "add",
          text: "\ths, err := noise.NewHandshakeState(noise.Config{",
          new_line: 153,
        },
        {
          kind: "add",
          text: "\t\tCipherSuite:   suite,",
          new_line: 154,
        },
        {
          kind: "add",
          text: "\t\tPattern:       noise.HandshakeIK,",
          new_line: 155,
        },
        {
          kind: "add",
          text: "\t\tInitiator:     false,",
          new_line: 156,
        },
        {
          kind: "add",
          text: "\t\tPrologue:      []byte(prologue),",
          new_line: 157,
        },
        {
          kind: "add",
          text: "\t\tStaticKeypair: r.Identity.noiseKey,",
          new_line: 158,
        },
        {
          kind: "add",
          text: "\t\tRandom:        rnd,",
          new_line: 159,
        },
        {
          kind: "add",
          text: "\t})",
          new_line: 160,
        },
      ],
    },
  },
  {
    id: "h_relay",
    path: "oss/internal/remote/relay.go",
    change: "added",
    role: "code",
    new_range: [484, 15],
    additions: 15,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_relay",
    diff: {
      header: "@@ -0,0 +484,15 @@",
      lines: [
        {
          kind: "add",
          text: "// wanted reports whether anything needs the relay: a paired device or a live",
          new_line: 484,
        },
        {
          kind: "add",
          text: "// offer. When the devices cannot be read it says no unless an offer is live or",
          new_line: 485,
        },
        {
          kind: "add",
          text: "// a relay session is up, and returns the error category.",
          new_line: 486,
        },
        {
          kind: "add",
          text: "func (m *Manager) wanted() (bool, string) {",
          new_line: 487,
        },
        {
          kind: "add",
          text: "\tnow := m.clock.Now()",
          new_line: 488,
        },
        {
          kind: "add",
          text: "\toffers := m.opts.Offers.PendingRelayTokenHashes(now)",
          new_line: 489,
        },
        {
          kind: "add",
          text: "\tdevices, err := m.opts.Devices.RelayTokenHashes()",
          new_line: 490,
        },
        {
          kind: "add",
          text: "\tif err != nil {",
          new_line: 491,
        },
        {
          kind: "add",
          text: "\t\tm.mu.Lock()",
          new_line: 492,
        },
        {
          kind: "add",
          text: "\t\tup := m.rs != nil",
          new_line: 493,
        },
        {
          kind: "add",
          text: "\t\tm.mu.Unlock()",
          new_line: 494,
        },
        {
          kind: "add",
          text: "\t\treturn len(offers) > 0 || up, ErrStore",
          new_line: 495,
        },
        {
          kind: "add",
          text: "\t}",
          new_line: 496,
        },
        {
          kind: "add",
          text: '\treturn len(devices)+len(offers) > 0, ""',
          new_line: 497,
        },
        {
          kind: "add",
          text: "}",
          new_line: 498,
        },
      ],
    },
  },
  {
    id: "h_redirect",
    path: "oss/internal/remote/relay.go",
    change: "added",
    role: "code",
    new_range: [538, 27],
    additions: 27,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_redirect",
  },
  {
    id: "h_serve",
    path: "oss/internal/remote/serve.go",
    change: "added",
    role: "code",
    new_range: [199, 10],
    additions: 10,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_serve",
    diff: {
      header: "@@ -0,0 +199,10 @@",
      lines: [
        {
          kind: "add",
          text: "\tplain, err := c.opts.Session.Decrypt(msg)",
          new_line: 199,
        },
        {
          kind: "add",
          text: "\tif err != nil {",
          new_line: 200,
        },
        {
          kind: "add",
          text: "\t\treturn ReasonDecrypt",
          new_line: 201,
        },
        {
          kind: "add",
          text: "\t}",
          new_line: 202,
        },
        {
          kind: "add",
          text: "\tif !c.active {",
          new_line: 203,
        },
        {
          kind: "add",
          text: "\t\tc.active = true",
          new_line: 204,
        },
        {
          kind: "add",
          text: "\t\tif c.opts.OnActive != nil {",
          new_line: 205,
        },
        {
          kind: "add",
          text: "\t\t\tc.opts.OnActive()",
          new_line: 206,
        },
        {
          kind: "add",
          text: "\t\t}",
          new_line: 207,
        },
        {
          kind: "add",
          text: "\t}",
          new_line: 208,
        },
      ],
    },
  },
  {
    id: "h_remote_test",
    path: "oss/internal/uiserver/remote_test.go",
    change: "added",
    role: "test",
    new_range: [1, 20],
    additions: 20,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_remote_test",
    diff: {
      header: "@@ -0,0 +1,20 @@",
      lines: [
        {
          kind: "add",
          text: "package uiserver",
          new_line: 1,
        },
        {
          kind: "add",
          text: "",
          new_line: 2,
        },
        {
          kind: "add",
          text: "import (",
          new_line: 3,
        },
        {
          kind: "add",
          text: '\t"bufio"',
          new_line: 4,
        },
        {
          kind: "add",
          text: '\t"context"',
          new_line: 5,
        },
        {
          kind: "add",
          text: '\t"encoding/json"',
          new_line: 6,
        },
        {
          kind: "add",
          text: '\t"go/ast"',
          new_line: 7,
        },
        {
          kind: "add",
          text: '\t"go/parser"',
          new_line: 8,
        },
        {
          kind: "add",
          text: '\t"go/token"',
          new_line: 9,
        },
        {
          kind: "add",
          text: '\t"io"',
          new_line: 10,
        },
        {
          kind: "add",
          text: '\t"net/http"',
          new_line: 11,
        },
        {
          kind: "add",
          text: '\t"net/http/httptest"',
          new_line: 12,
        },
        {
          kind: "add",
          text: '\t"net/url"',
          new_line: 13,
        },
        {
          kind: "add",
          text: '\t"os"',
          new_line: 14,
        },
        {
          kind: "add",
          text: '\t"path/filepath"',
          new_line: 15,
        },
        {
          kind: "add",
          text: '\t"regexp"',
          new_line: 16,
        },
        {
          kind: "add",
          text: '\t"strconv"',
          new_line: 17,
        },
        {
          kind: "add",
          text: '\t"strings"',
          new_line: 18,
        },
        {
          kind: "add",
          text: '\t"sync"',
          new_line: 19,
        },
        {
          kind: "add",
          text: '\t"testing"',
          new_line: 20,
        },
      ],
    },
  },
  {
    id: "h_security",
    path: "oss/SECURITY.md",
    change: "added",
    role: "docs",
    new_range: [1246, 15],
    additions: 15,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_security",
    diff: {
      header: "@@ -0,0 +1246,15 @@",
      lines: [
        {
          kind: "add",
          text: "What the relay can see: the daemon's relay public key and channel, the hashes",
          new_line: 1246,
        },
        {
          kind: "add",
          text: "of relay tokens, the token a phone presents, when the daemon and phones connect",
          new_line: 1247,
        },
        {
          kind: "add",
          text: "and disconnect, the size and number of messages, and IP addresses. It cannot",
          new_line: 1248,
        },
        {
          kind: "add",
          text: "see any key that decrypts, any plaintext, a request path, header or body, or a",
          new_line: 1249,
        },
        {
          kind: "add",
          text: "review. A relay cannot read or change a message undetected: the phone and the",
          new_line: 1250,
        },
        {
          kind: "add",
          text: "daemon share keys from a Noise handshake (`Noise_IK_25519_ChaChaPoly_SHA256`)",
          new_line: 1251,
        },
        {
          kind: "add",
          text: "that the relay is not part of, and a relay token only gets a phone to the",
          new_line: 1252,
        },
        {
          kind: "add",
          text: "relay's door. A phone is known by its key. A pairing offer holds a one-time",
          new_line: 1253,
        },
        {
          kind: "add",
          text: "secret in a QR code that lasts 5 minutes, and is never logged or stored. The",
          new_line: 1254,
        },
        {
          kind: "add",
          text: "secret can pair a phone only once, but the phone's relay token is derived from",
          new_line: 1255,
        },
        {
          kind: "add",
          text: "it. Anyone who photographed the code within its five minutes can afterwards",
          new_line: 1256,
        },
        {
          kind: "add",
          text: "open connections to the relay for that device. They cannot finish the",
          new_line: 1257,
        },
        {
          kind: "add",
          text: "encrypted handshake or read anything, but they can occupy connection slots and",
          new_line: 1258,
        },
        {
          kind: "add",
          text: "so keep the real phone from connecting until that phone is removed and paired",
          new_line: 1259,
        },
        {
          kind: "add",
          text: "again.",
          new_line: 1260,
        },
      ],
    },
  },
  {
    id: "h_gosum",
    path: "oss/go.sum",
    change: "modified",
    role: "generated",
    new_range: [12, 9],
    additions: 2,
    deletions: 0,
    github_url: "https://github.com/acme/demo/pull/7/files#h_gosum",
    diff: {
      header: "@@ -12,7 +12,9 @@",
      lines: [
        {
          kind: "context",
          text: "github.com/cli/safeexec v1.0.1/go.mod h1:Z/D4tTN8Vs5gXYHDCbaM1S/anmEDnJb1iW0+EJ5zx3Q=",
          old_line: 12,
          new_line: 12,
        },
        {
          kind: "context",
          text: "github.com/cli/shurcooL-graphql v0.0.4 h1:6MogPnQJLjKkaXPyGqPRXOI2qCsQdqNfUY1QSJu2GuY=",
          old_line: 13,
          new_line: 13,
        },
        {
          kind: "context",
          text: "github.com/cli/shurcooL-graphql v0.0.4/go.mod h1:3waN4u02FiZivIV+p1y4d0Jo1jc6BViMA73C+sZo2fk=",
          old_line: 14,
          new_line: 14,
        },
        {
          kind: "add",
          text: "github.com/coder/websocket v1.8.15 h1:6B2JPeOGlpff2Uz6vOEH1Vzpi0iUz20A+lPVhPHtNUA=",
          new_line: 15,
        },
        {
          kind: "add",
          text: "github.com/coder/websocket v1.8.15/go.mod h1:NX3SzP+inril6yawo5CQXx8+fk145lPDC6pumgx0mVg=",
          new_line: 16,
        },
        {
          kind: "context",
          text: "github.com/cpuguy83/go-md2man/v2 v2.0.6/go.mod h1:oOW0eioCTA6cOiMLiUPZOpcVxMig6NIQQ7OS05n1F4g=",
          old_line: 15,
          new_line: 17,
        },
        {
          kind: "context",
          text: "github.com/danieljoos/wincred v1.2.2 h1:774zMFJrqaeYCK2W57BgAem/MLi6mtSE47MB6BOJ0i0=",
          old_line: 16,
          new_line: 18,
        },
        {
          kind: "context",
          text: "github.com/danieljoos/wincred v1.2.2/go.mod h1:w7w4Utbrz8lqeMbDAK0lkNJUv5sAOkFi7nd/ogr0Uh8=",
          old_line: 17,
          new_line: 19,
        },
        {
          kind: "context",
          text: "github.com/dustin/go-humanize v1.0.1 h1:GzkhY7T5VNhEkwH0PVJgjz+fX1rhBrR7pRT3mDkpeCY=",
          old_line: 18,
          new_line: 20,
        },
      ],
    },
  },
];
