import { useLayoutEffect, useRef } from "react";

/** A native dialog that opens modally when it mounts; its parent unmounts it.
 *  Closing the element as it unmounts lets the browser give the focus back to
 *  what had it before the dialog opened. */
export function useModal() {
  const dialog = useRef<HTMLDialogElement>(null);
  useLayoutEffect(() => {
    const element = dialog.current;
    element?.showModal();
    return () => {
      if (element?.open) element.close();
    };
  }, []);
  return dialog;
}
