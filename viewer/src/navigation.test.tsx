import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import {
  UrlStateProvider,
  useUrlState,
  ViewerLink,
  ViewerLinkProvider,
  type ViewerLinkProps,
} from "./navigation";

function HostLink({ href, children, ...props }: ViewerLinkProps) {
  return (
    <a data-host-link="" href={`/app${href}`} {...props}>
      {children}
    </a>
  );
}

function Search() {
  return <span>{useUrlState().search.toString()}</span>;
}

describe("navigation", () => {
  it("renders a plain anchor without a host link", () => {
    expect(renderToStaticMarkup(<ViewerLink href="/a">A</ViewerLink>)).toBe('<a href="/a">A</a>');
  });

  it("renders links through the host's link component", () => {
    const html = renderToStaticMarkup(
      <ViewerLinkProvider link={HostLink}>
        <ViewerLink href="/a">A</ViewerLink>
        <ViewerLink href="/b">B</ViewerLink>
      </ViewerLinkProvider>,
    );
    expect(html).toContain('data-host-link="" href="/app/a"');
    expect(html).toContain('data-host-link="" href="/app/b"');
  });

  it("reads the host's search params, and refuses to guess without them", () => {
    const value = { search: new URLSearchParams("review=2"), update: () => {} };
    expect(
      renderToStaticMarkup(
        <UrlStateProvider value={value}>
          <Search />
        </UrlStateProvider>,
      ),
    ).toBe("<span>review=2</span>");
    expect(() => renderToStaticMarkup(<Search />)).toThrow(/UrlStateProvider/);
  });
});
