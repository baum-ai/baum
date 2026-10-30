import type { UrlState } from "@baum/viewer/navigation";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { appURL } from "./api";

/** The address's search parameters as the viewer's URL state. A write adds
 *  a history entry, or with `replace` takes the current one's place, so Back
 *  skips it (as a merged feature's redirect needs). */
export function useLocation() {
  const [search, setSearch] = useState(() => new URLSearchParams(location.search));
  const current = useRef(search);
  const update = useCallback<UrlState["update"]>((patch, options) => {
    const next = patch(new URLSearchParams(current.current));
    current.current = next;
    if (options?.replace) history.replaceState(null, "", appURL(`?${next}`));
    else history.pushState(null, "", appURL(`?${next}`));
    setSearch(next);
  }, []);
  useEffect(() => {
    const pop = () => {
      current.current = new URLSearchParams(location.search);
      setSearch(current.current);
    };
    addEventListener("popstate", pop);
    return () => removeEventListener("popstate", pop);
  }, []);
  return useMemo(() => ({ search, update }), [search, update]);
}
