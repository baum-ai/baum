// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { reviewDocumentFor } from "../../test/review-document";
import { reviewRefs, type ReviewDocument, type ReviewRef } from "../review-document";
import { Markdown } from "./markdown";
import { ReviewJumpContext, ReviewRefsContext, type ReviewJump } from "./review-contexts";

const SESSION = "785faefe-40b5-5521-9932-4dc2d659ec1d";

function documentWithIndex() {
  const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  document.index.threads = [
    {
      id: "PRRT_kwDOabc-1",
      path: "web/app/routes/activity.tsx",
      line: 28,
      resolved: false,
      url: "https://github.com/acme/widgets/pull/42#discussion_r1",
    },
    { id: "PRRT_done", path: null, line: null, resolved: true },
  ];
  document.index.checks[0]!.id = "check_run:9001";
  document.index.checks.push({
    id: "status:77",
    kind: "status",
    name: "deploy",
    conclusion: "failure",
    required: false,
    head: document.index.checks[0]!.head,
    url: "https://github.com/other/repo/actions/runs/5",
  });
  document.index.checks.push({
    id: "check_run:5",
    kind: "check_run",
    name: "lint",
    conclusion: "failure",
    required: false,
    head: "0000000000000000000000000000000000000000",
  });
  document.sections[1]!.id = "sec_behavior";
  document.index.sessions[0]!.id = SESSION;
  document.index.sessions[0]!.title = "Add repository filters";
  return document;
}

const REFS = reviewRefs(documentWithIndex());

const render = (text: string, refs: ReadonlyMap<string, ReviewRef> | null = REFS) =>
  renderToStaticMarkup(
    <ReviewRefsContext.Provider value={refs}>
      <Markdown>{text}</Markdown>
    </ReviewRefsContext.Provider>,
  );

describe("reference chips in review prose", () => {
  it("names a risk, evidence and a section by their text and title", () => {
    const html = render("See rsk_old, ev_1 and sec_behavior; also dec_1.");
    // Item IDs name the first item of that ID in section order.
    expect(html).toContain('data-review-ref="item"');
    expect(html).toContain('title="Risk rsk_old"');
    expect(html).toContain('title="Evidence ev_1"');
    expect(html).toContain('title="Decision dec_1"');
    expect(html).toContain('title="Section sec_behavior"');
    expect(html).not.toContain("rsk_old,");
  });

  it("leaves an unknown ID as written", () => {
    const html = render("Not rsk_missing, PRRT_nope, check_run:1 or comment 5.");
    expect(html).not.toContain("data-review-ref");
    expect(html).toContain("rsk_missing");
    expect(html).toContain("PRRT_nope");
    expect(html).toContain("check_run:1");
    expect(html).toContain("comment 5");
  });

  it("shows a check with its conclusion and links it to its run", () => {
    const html = render("Failed in check_run:9001.");
    expect(html).toContain('data-review-ref="check"');
    expect(html).toContain(">UI<");
    expect(html).toContain(">Success<");
    expect(html).toContain("bg-status-positive");
    expect(html).toContain('href="https://github.com/acme/widgets/actions/runs/1/job/1"');
  });

  it("drops a check link into another repository and keeps the chip plain", () => {
    const html = render("see status:77");
    expect(html).toContain(">deploy<");
    expect(html).toContain(">Failure<");
    expect(html).toContain("bg-status-critical");
    expect(html).not.toContain("other/repo");
    expect(html).toContain("no link available");
    expect(html).not.toContain("<a ");
  });

  it("does not show a check from an earlier head as current", () => {
    const html = render("Was red in check_run:5.");
    expect(html).toContain(">lint<");
    expect(html).toContain(">earlier head<");
    expect(html).toContain('title="Check lint: earlier head"');
    expect(html).not.toContain(">Failure<");
    expect(html).not.toContain("bg-status-critical");
  });

  it("shows a thread as its place and state, linked to it", () => {
    const html = render("Open: PRRT_kwDOabc-1. Closed: PRRT_done.");
    expect(html).toContain(">activity.tsx:28<");
    expect(html).toContain(">open<");
    expect(html).toContain('href="https://github.com/acme/widgets/pull/42#discussion_r1"');
    expect(html).toContain(">resolved<");
  });

  it("shows a comment by its author, from its words or a bare code span", () => {
    const html = render("As in comment 4100000001 and `4100000002`; not `123`.");
    expect(html).toContain("Comment by morgan");
    expect(html).toContain("Comment by cursor");
    expect(html).toContain('href="https://github.com/acme/widgets/pull/42#discussion_r4100000001"');
    expect(html).toContain("<code");
    expect(html).toContain(">123<");
  });

  it("shows a session by its title", () => {
    const html = render(`Done in ${SESSION}.`);
    expect(html).toContain('data-review-ref="session"');
    expect(html).toContain(">Add repository filters<");
    expect(html).not.toContain(SESSION + "<");
  });

  it("keeps links, code blocks and text outside a review as written", () => {
    const html = render("[rsk_old](https://e.com)\n\n```\nrsk_old\n```");
    expect(html).not.toContain("data-review-ref");
    expect(html.match(/rsk_old/g)).toHaveLength(2);
    expect(renderToStaticMarkup(<Markdown>{"cites rsk_old"}</Markdown>)).toContain("cites rsk_old");
  });

  it("does not let a comment's markup forge a chip", () => {
    const html = render(
      '<a data-ref="rsk_old" href="https://evil.example">x</a> <a data-ref="check_run:9001">y</a>',
    );
    expect(html).not.toContain("data-review-ref");
    expect(html).toContain("https://evil.example");
  });

  describe("opening", () => {
    const roots: Root[] = [];
    afterEach(() => act(() => roots.splice(0).forEach((root) => root.unmount())));

    const mount = (text: string, jump: ReviewJump | null) => {
      const host = document.createElement("div");
      document.body.append(host);
      const root = createRoot(host);
      roots.push(root);
      act(() =>
        root.render(
          <ReviewRefsContext.Provider value={REFS}>
            <ReviewJumpContext.Provider value={jump}>
              <Markdown>{text}</Markdown>
            </ReviewJumpContext.Provider>
          </ReviewRefsContext.Provider>,
        ),
      );
      return host;
    };

    it("takes an item to its section and a section to its top", () => {
      const jump = { item: vi.fn() };
      const host = mount("rsk_old and sec_behavior", jump);
      const chips = host.querySelectorAll("button");
      act(() => chips[0]!.click());
      expect(jump.item).toHaveBeenLastCalledWith("overview", "rsk_old");
      act(() => chips[1]!.click());
      expect(jump.item).toHaveBeenLastCalledWith("sec_behavior", undefined);
    });

    it("takes a session to the sessions the host lists, and is plain without them", () => {
      const session = vi.fn();
      const host = mount(SESSION, { item: vi.fn(), session });
      act(() => host.querySelector("button")!.click());
      expect(session).toHaveBeenCalledWith(SESSION);
      const plain = mount(`${SESSION} rsk_old`, null);
      expect(plain.querySelector("button")).toBeNull();
      expect(plain.querySelectorAll("[data-review-ref]")).toHaveLength(2);
    });
  });
});
