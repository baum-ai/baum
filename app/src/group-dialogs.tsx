import { useState, type FormEvent } from "react";
import { ViewerLink, useUrlState } from "@baum/viewer/navigation";
import { Alert, Button } from "@baum/viewer/components/ui";
import { Empty } from "@baum/viewer/components/states";
import { ChevronLeftIcon } from "@baum/viewer/components/icons";
import { SMALL } from "./action-classes";
import { backTo } from "./api";
import { useModal } from "./modal";
import { PageBar } from "./shell";

// Kept apart from group.tsx so the always-loaded app can show these dialogs
// without pulling in the lazily loaded group page and the review viewer.
const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "Local request failed.";

/** Asks for a group's name: when creating one and when renaming it. */
export function NameDialog({
  title,
  detail,
  initial,
  action,
  submit,
  close,
}: {
  title: string;
  /** What the group is for, under the title. */
  detail?: string;
  initial: string;
  action: string;
  submit: (name: string) => Promise<void>;
  close: () => void;
}) {
  const dialog = useModal();
  const [name, setName] = useState(initial);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const send = async (event: FormEvent) => {
    event.preventDefault();
    setPending(true);
    setError("");
    try {
      await submit(name.trim());
    } catch (e) {
      setError(errorText(e));
      setPending(false);
    }
  };
  return (
    <dialog ref={dialog} onCancel={close} aria-labelledby="group-name-title">
      <form onSubmit={(event) => void send(event)}>
        <div className="grid gap-1">
          <h2 id="group-name-title" className="text-lg font-semibold">
            {title}
          </h2>
          {detail ? <p className="text-xs text-secondary">{detail}</p> : null}
        </div>
        <label className="local-field-label">
          Group name
          <input
            type="text"
            className="local-field"
            autoFocus
            value={name}
            maxLength={80}
            onChange={(event) => setName(event.target.value)}
          />
        </label>
        {error ? <Alert>{error}</Alert> : null}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" size="compact" onClick={close}>
            Cancel
          </Button>
          <Button type="submit" size="compact" disabled={pending || !name.trim()}>
            {pending ? "Saving…" : action}
          </Button>
        </div>
      </form>
    </dialog>
  );
}
/** Asks before an action that cannot simply be taken back. */
export function ConfirmDialog({
  title,
  children,
  action,
  pending,
  confirm,
  close,
  danger = false,
}: {
  title: string;
  children: string;
  action: string;
  pending: boolean;
  /** The action removes something: drawn in the critical tone. */
  danger?: boolean;
  confirm: () => void;
  close: () => void;
}) {
  const dialog = useModal();
  return (
    <dialog ref={dialog} onCancel={close} aria-label={title}>
      <h2 className="text-lg font-semibold">{title}</h2>
      <p className="my-3">{children}</p>
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="compact" disabled={pending} onClick={close}>
          Cancel
        </Button>
        <Button
          size="compact"
          variant={danger ? "danger" : "primary"}
          disabled={pending}
          onClick={confirm}
        >
          {action}
        </Button>
      </div>
    </dialog>
  );
}

/** The page of a group id the daemon does not have, or no longer has. */
export function GroupMissing() {
  const back = backTo(useUrlState().search);
  return (
    <section>
      <PageBar
        title="Review group not found"
        back={
          <ViewerLink
            href={back.href}
            className="local-page-back"
            aria-label={`Back to ${back.label}`}
            title={`Back to ${back.label} (Esc)`}
          >
            <ChevronLeftIcon size={18} />
            <kbd aria-hidden="true" className="key-hint">
              Esc
            </kbd>
          </ViewerLink>
        }
      />
      <div className="local-page-body">
        <Empty
          variant="page"
          title="This review group no longer exists"
          action={
            <ViewerLink href={back.href} className={SMALL}>
              Go to {back.label}
            </ViewerLink>
          }
        >
          Its pull requests and saved reviews are kept.
        </Empty>
      </div>
    </section>
  );
}
