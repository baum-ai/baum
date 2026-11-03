import { CircleCheckIcon, CloseIcon, WarningIcon } from "@baum/viewer/components/icons";
import { BrandMark } from "@baum/viewer/components/brand-logo";
import type { Brand } from "@baum/viewer/brands";
import { SMALL } from "./action-classes";
import { cn } from "@baum/viewer/components/classnames";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";

/** What the app tells the person after they acted: what happened, what it
 *  was about, and where (a session, a pull request), with that thing's icon;
 *  `error` draws a failure, and `action` offers one step more, such as Undo.
 *  A `sticky` toast stays until the person closes it or takes its action,
 *  either of which calls `onClose`. */
export type Toast = {
  title: string;
  detail?: string;
  context?: string;
  /** The context's vendor mark and its name for assistive technology. */
  icon?: { brand: Brand; alt: string };
  error?: boolean;
  action?: { label: string; run: () => void };
  sticky?: boolean;
  onClose?: () => void;
};

const Show = createContext<(toast: Toast) => void>(() => {});

/** Shows a toast over the window's foot: `useToast()(toast)`. */
export const useToast = () => useContext(Show);

/** The app's toasts: one at a time, the latest replacing the one before,
 *  each gone after a few seconds or when dismissed, in a live region that
 *  reads it out. */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toast, setToast] = useState<(Toast & { id: number }) | null>(null);
  const next = useRef(0);
  const show = useCallback((t: Toast) => setToast({ ...t, id: ++next.current }), []);
  return (
    <Show.Provider value={show}>
      {children}
      <div aria-live="polite" className="local-toast-live">
        {toast ? <ToastView key={toast.id} toast={toast} close={() => setToast(null)} /> : null}
      </div>
    </Show.Provider>
  );
}

/** How long a toast stays, in milliseconds. */
const SHOWN_FOR = 6000;

function ToastView({ toast, close }: { toast: Toast; close: () => void }) {
  const closing = useRef(close);
  closing.current = close;
  useEffect(() => {
    if (toast.sticky) return;
    const timer = setTimeout(() => closing.current(), SHOWN_FOR);
    return () => clearTimeout(timer);
  }, [toast.sticky]);
  const dismiss = () => {
    toast.onClose?.();
    close();
  };
  const Icon = toast.error ? WarningIcon : CircleCheckIcon;
  return (
    <div className={toast.error ? "local-toast error" : "local-toast"}>
      <Icon size={16} className="local-toast-icon" />
      <div className="local-toast-body">
        <p className="local-toast-title">{toast.title}</p>
        {toast.detail ? <p className="local-toast-detail">{toast.detail}</p> : null}
        {toast.context ? (
          <p className="local-toast-context">
            {toast.icon ? (
              <span role="img" aria-label={toast.icon.alt}>
                <BrandMark brand={toast.icon.brand} size={14} />
              </span>
            ) : null}
            {toast.context}
          </p>
        ) : null}
      </div>
      {toast.action ? (
        <button
          type="button"
          className={cn("local-toast-action", SMALL)}
          onClick={() => {
            toast.action!.run();
            dismiss();
          }}
        >
          {toast.action.label}
        </button>
      ) : null}
      <button type="button" className="local-toast-close" aria-label="Dismiss" onClick={dismiss}>
        <CloseIcon size={12} />
      </button>
    </div>
  );
}
