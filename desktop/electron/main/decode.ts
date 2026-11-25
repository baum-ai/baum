// Reads JSON the way the Rust shell's serde types did: unknown fields are
// ignored, a `#[serde(default)]` field may be missing but not null, an
// Option may be missing or null, and a wrong type is an error.

export type Fields = Record<string, unknown>;

export function object(value: unknown, what: string): Fields {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`invalid type: expected ${what}`);
  }
  return value as Fields;
}

function wrong(key: string, expected: string): Error {
  return new Error(`invalid type for \`${key}\`: expected ${expected}`);
}

export function string(o: Fields, key: string): string {
  const v = o[key];
  if (v === undefined) throw new Error(`missing field \`${key}\``);
  if (typeof v !== "string") throw wrong(key, "a string");
  return v;
}

export function stringOr(o: Fields, key: string, fallback = ""): string {
  return o[key] === undefined ? fallback : string(o, key);
}

export function optionalString(o: Fields, key: string): string | null {
  const v = o[key];
  return v === undefined || v === null ? null : string(o, key);
}

export function bool(o: Fields, key: string): boolean {
  const v = o[key];
  if (v === undefined) throw new Error(`missing field \`${key}\``);
  if (typeof v !== "boolean") throw wrong(key, "a boolean");
  return v;
}

export function boolOr(o: Fields, key: string, fallback = false): boolean {
  return o[key] === undefined ? fallback : bool(o, key);
}

export function optionalBool(o: Fields, key: string): boolean | null {
  const v = o[key];
  return v === undefined || v === null ? null : bool(o, key);
}

export function arrayOr<T>(o: Fields, key: string, item: (v: unknown) => T): T[] {
  const v = o[key];
  if (v === undefined) return [];
  if (!Array.isArray(v)) throw wrong(key, "a sequence");
  return v.map(item);
}

export function optional<T>(o: Fields, key: string, read: (v: unknown) => T): T | null {
  const v = o[key];
  return v === undefined || v === null ? null : read(v);
}

export function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
