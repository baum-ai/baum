// The shell's log. Every line goes through `redact`, so a launch URL, a
// credential or a bearer token that reaches a message by mistake is never
// written out.

/** `message` without the daemon's launch capability: a `token=` fragment or
 *  parameter and a bearer token are replaced, whatever surrounds them. */
export function redact(message: string): string {
  return message
    .replace(/([#&?]token=)[^\s"'&#]*/gi, "$1<redacted>")
    .replace(/(Bearer\s+)[^\s"']+/gi, "$1<redacted>");
}

export function log(message: string): void {
  console.error(`baum desktop: ${redact(message)}`);
}

export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}
