import { useEffect, useRef, useState } from "react";
import "./terminal-setup.css";

/** Copies text, and says for two seconds whether that worked. */
export function useCopy() {
  const [copied, setCopied] = useState("");
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const copy = async (text: string) => {
    clearTimeout(timer.current);
    try {
      await navigator.clipboard.writeText(text);
      setCopied("Copied");
    } catch {
      setCopied("Could not copy");
    }
    timer.current = setTimeout(() => setCopied(""), 2000);
  };
  return { copied, copy };
}

/** The `baum desktop-setup` command the user runs in their own terminal, as
 *  setup shows it, with a button that copies it. The command names the app's
 *  baum by its full path, so it is shown as is: `~` would not expand inside
 *  its quotes. */
export function TerminalSetup({ text, command }: { text: string; command: string }) {
  const { copied, copy } = useCopy();
  return (
    <div id="terminal-setup" className="setup-card terminal-setup">
      <p>{text}</p>
      <div className="terminal-command">
        <code id="terminal-setup-command">{command}</code>
        <button
          id="terminal-setup-copy"
          type="button"
          className="secondary compact"
          aria-label="Copy the command"
          onClick={() => void copy(command)}
        >
          {copied || "Copy"}
        </button>
      </div>
      <span className="visually-hidden" role="status">
        {copied}
      </span>
    </div>
  );
}
