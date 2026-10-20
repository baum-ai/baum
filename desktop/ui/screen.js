// The bundled connecting screen. The app reports the connection state through
// its `connection` command; this page only renders it and asks to reconnect.
// withGlobalTauri is off so the daemon's pages get no Tauri globals, which
// leaves the core IPC bridge Tauri injects into its own pages.
const invoke = (command) => window.__TAURI_INTERNALS__.invoke(command);

const connecting = document.getElementById("connecting");
const failed = document.getElementById("failed");
const message = document.getElementById("message");
const details = document.getElementById("details");
const log = document.getElementById("log");
const button = document.getElementById("reconnect");

function render(connection) {
  const isFailed = connection.phase === "failed";
  connecting.hidden = isFailed;
  failed.hidden = !isFailed;
  if (!isFailed) return;
  message.textContent = connection.message;
  log.textContent = connection.details.join("\n");
  details.hidden = connection.details.length === 0;
  button.disabled = false;
}

async function poll() {
  try {
    render(await invoke("connection"));
  } catch (error) {
    render({ phase: "failed", message: String(error), details: [] });
  }
  setTimeout(poll, 500);
}

button.addEventListener("click", () => {
  button.disabled = true;
  render({ phase: "connecting" });
  invoke("reconnect").catch((error) =>
    render({ phase: "failed", message: String(error), details: [] }),
  );
});

poll();
