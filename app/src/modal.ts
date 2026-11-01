import { useLayoutEffect, useRef } from "react";

/** A native dialog that opens modally when it mounts (or when `open` turns
 *  true); its parent unmounts it. Closing the element as it unmounts lets the
 *  browser give the focus back to what had it before the dialog opened; when
 *  that is gone, the page's keys still act on its selection. */
export function useModal(open = true) {
  const dialog = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const element = dialog.current;
    if (!open || !element) return;
    element.showModal();
    return () => {
      if (element.open) element.close();
    };
  }, [open]);
  return dialog;
}
