# Product video

The video on usebaum.com (`ui/app/assets/baum-app.mp4`) is a recording of this
app, not a mock-up. The video fixture daemon (`TestLocalAppVideoFixture` in
`../../internal/uiserver/video_fixture_test.go`) runs the real daemon with
made-up work: the fernhill repositories, their pull requests and reviews,
four Timeline features, and Claude and Codex plan usage. Its fix of
fernhill/api#379 really runs, pushing to a local bare repository in place of
GitHub. Five Claude Code and Codex sessions are live on its pull requests.
`demo.spec.ts` walks through it: the Inbox, a new Work inbox, a fix, the
Sessions overview, the Pull requests list and a Timeline feature. It marks
where each part starts and measures where the sidebar's Inbox, Pull requests,
Timeline and Sessions links are, and `encode.mjs` writes both, in the video's
seconds, to `ui/app/assets/baum-app-chapters.ts`: on the website a click on
one of those links in the video plays it from that part. The website shows
the dark recording when the page is in dark mode, and on a switch carries on
from the same moment in the other.

Record it again after a change to the app that the video shows. From the
repository root:

```sh
# The app the fixture embeds, then the fixture.
BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 ./dev-compose.sh run --build --rm --no-deps --workdir /oss/app ui-test pnpm build
BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 ./dev-compose.sh run --build --rm --no-deps oss-test sh -ec 'CGO_ENABLED=0 go test -c ./internal/uiserver -o app/e2e/video-fixture'

# The walkthrough, into video/frames, then the MP4, its poster and chapters,
# with ffmpeg: the light recording, then the dark one (baum-app-dark).
BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 ./dev-compose.sh run --build --rm --no-deps --workdir /oss/app ui-test ./node_modules/.bin/playwright test -c playwright.video.config.ts
docker run --rm -v "$PWD":/repo -w /repo/oss/app node:24 sh -c 'apt-get -qq update && apt-get -qq install -y ffmpeg && node video/encode.mjs /repo/ui/app/assets'
BAUM_OP_RUN=0 BAUM_DEV_HEAVY=1 ./dev-compose.sh run --build --rm --no-deps -e BAUM_VIDEO_SCHEME=dark --workdir /oss/app ui-test ./node_modules/.bin/playwright test -c playwright.video.config.ts
docker run --rm -v "$PWD":/repo -w /repo/oss/app node:24 sh -c 'apt-get -qq update && apt-get -qq install -y ffmpeg && node video/encode.mjs /repo/ui/app/assets baum-app-dark'
```

The video plays 1.6 times as fast as the walkthrough (`SPEED` in
`encode.mjs`). The app is recorded at 1056 by 800, the width of the website's
column, at twice the pixels, so the website shows it one CSS pixel to one and its text
reads at the app's own size. Screenshots taken in a loop, about 18 a second,
keep that density, which Chrome's screencast does not.
