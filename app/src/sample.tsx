import { useEffect } from "react";
import { inSample } from "./api";
import { useToast } from "./toast";

/** In the tour's sample project the daemon refuses whatever would change
 *  something, marking the answer; this says so in a toast. Links out of the
 *  app are held back too, since the sample's pull requests and repositories
 *  do not exist. Outside the sample it does nothing. */
export function SampleGuard() {
  const toast = useToast();
  useEffect(() => {
    if (!inSample()) return;
    const refused = () =>
      toast({
        title: "Not in the sample project",
        detail: "It's here for the tour, so nothing changed.",
      });
    const fetching = window.fetch;
    window.fetch = async (...args) => {
      const response = await fetching(...args);
      if (response.headers.get("X-Baum-Sample") === "refused") refused();
      return response;
    };
    const click = (event: MouseEvent) => {
      const link = (event.target as Element | null)?.closest?.("a[href]");
      if (link instanceof HTMLAnchorElement && new URL(link.href).origin !== location.origin) {
        event.preventDefault();
        event.stopPropagation();
        refused();
      }
    };
    addEventListener("click", click, true);
    return () => {
      window.fetch = fetching;
      removeEventListener("click", click, true);
    };
  }, [toast]);
  return null;
}
