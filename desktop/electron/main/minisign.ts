// Verifies minisign signatures, the format of the release key's updater
// signatures (scripts/updater-sign.sh). A public key file is an untrusted
// comment line and the base64 of "Ed", an 8-byte key ID and the 32-byte
// Ed25519 key. A signature file is an untrusted comment line, the base64 of
// the algorithm ("ED": the Ed25519 signature is over the file's BLAKE2b-512
// hash; legacy "Ed": over the file itself), the key ID and the 64-byte
// signature, then a "trusted comment: " line and the base64 of a global
// signature over the signature and the trusted comment's text.
import { createPublicKey, verify, type KeyObject } from "node:crypto";
import { createReadStream } from "node:fs";
import { readFile } from "node:fs/promises";

export class MinisignError extends Error {}

export interface PublicKey {
  keyId: Buffer;
  key: KeyObject;
}

export interface Signature {
  algorithm: "ED" | "Ed";
  keyId: Buffer;
  signature: Buffer;
  trustedComment: string;
  globalSignature: Buffer;
}

const BASE64 = /^[A-Za-z0-9+/]+={0,2}$/;
const TRUSTED = "trusted comment: ";
// The DER SubjectPublicKeyInfo of an Ed25519 key, before its 32 bytes.
const ED25519_SPKI = Buffer.from("302a300506032b6570032100", "hex");

function base64(text: string, length: number, what: string): Buffer {
  const data = BASE64.test(text) ? Buffer.from(text, "base64") : null;
  if (data?.length !== length) throw new MinisignError(`${what} is not valid`);
  return data;
}

function lines(text: string): string[] {
  return text.split("\n").map((line) => line.replace(/\r$/, ""));
}

/** A minisign public key file, or just its base64 line. */
export function parsePublicKey(text: string): PublicKey {
  const all = lines(text.trim());
  const line = all[0]?.startsWith("untrusted comment:") ? all[1] : all[0];
  const data = base64(line ?? "", 42, "the minisign public key");
  if (data.subarray(0, 2).toString("latin1") !== "Ed") {
    throw new MinisignError("the minisign public key is not an Ed25519 key");
  }
  const key = createPublicKey({
    key: Buffer.concat([ED25519_SPKI, data.subarray(10)]),
    format: "der",
    type: "spki",
  });
  return { keyId: data.subarray(2, 10), key };
}

/** A minisign signature file. */
export function parseSignature(text: string): Signature {
  const [untrusted, encoded, trusted, global] = lines(text);
  if (!untrusted?.startsWith("untrusted comment:") || !trusted?.startsWith(TRUSTED)) {
    throw new MinisignError("the minisign signature is not valid");
  }
  const data = base64(encoded ?? "", 74, "the minisign signature");
  const algorithm = data.subarray(0, 2).toString("latin1");
  if (algorithm !== "ED" && algorithm !== "Ed") {
    throw new MinisignError(`unknown minisign signature algorithm ${JSON.stringify(algorithm)}`);
  }
  return {
    algorithm,
    keyId: data.subarray(2, 10),
    signature: data.subarray(10),
    trustedComment: trusted.slice(TRUSTED.length),
    globalSignature: base64(global ?? "", 64, "the minisign global signature"),
  };
}

/** A release's `.sig` file: the base64 of the minisign signature file, as
 *  scripts/updater-sign.sh writes it. */
export function parseReleaseSignature(text: string): Signature {
  const trimmed = text.trim();
  if (!BASE64.test(trimmed)) throw new MinisignError("the update signature is not base64");
  return parseSignature(Buffer.from(trimmed, "base64").toString("utf8"));
}

/** Whether the trusted comment names `version`: updater-sign.sh writes
 *  "timestamp:…<tab>file:…<tab>version:…". */
export function signedForVersion(signature: Signature, version: string): boolean {
  return signature.trustedComment.split("\t").includes(`version:${version}`);
}

function ed25519(key: PublicKey, data: Uint8Array, signature: Buffer): boolean {
  return verify(null, data, key.key, signature);
}

/** Checks everything but the signed data: the key, the trusted comment's
 *  global signature and the version it names. */
function checkSignature(key: PublicKey, signature: Signature, version: string): void {
  if (!signature.keyId.equals(key.keyId)) {
    throw new MinisignError(
      `signed with key ${hexId(signature.keyId)}, not the release key ${hexId(key.keyId)}`,
    );
  }
  const comment = Buffer.concat([signature.signature, Buffer.from(signature.trustedComment)]);
  if (!ed25519(key, comment, signature.globalSignature)) {
    throw new MinisignError("the signature's trusted comment is not signed by the release key");
  }
  if (!signedForVersion(signature, version)) {
    throw new MinisignError(
      `signed for ${JSON.stringify(signature.trustedComment)}, not version ${version}`,
    );
  }
}

// minisign prints key IDs as the little-endian number in hex.
function hexId(id: Buffer): string {
  return Buffer.from(id).reverse().toString("hex").toUpperCase();
}

/** Throws a MinisignError unless `data` carries `signature` from `key`, made
 *  for `version`. */
export function verifyData(
  data: Uint8Array,
  signature: Signature,
  key: PublicKey,
  version: string,
): void {
  checkSignature(key, signature, version);
  const signed = signature.algorithm === "ED" ? blake2b512(data) : data;
  if (!ed25519(key, signed, signature.signature)) {
    throw new MinisignError("the signature does not match the file");
  }
}

/** verifyData for a file; prehashed signatures stream it. */
export async function verifyFile(
  path: string,
  signature: Signature,
  key: PublicKey,
  version: string,
): Promise<void> {
  checkSignature(key, signature, version);
  let signed: Uint8Array;
  if (signature.algorithm === "ED") {
    const hash = new Blake2b512();
    for await (const chunk of createReadStream(path)) hash.update(chunk as Buffer);
    signed = hash.digest();
  } else {
    signed = await readFile(path);
  }
  if (!ed25519(key, signed, signature.signature)) {
    throw new MinisignError("the signature does not match the file");
  }
}

// BLAKE2b-512 (RFC 7693). Electron's BoringSSL has no BLAKE2b-512, so Node's
// createHash("blake2b512") fails in the app. 64-bit words are pairs of 32-bit
// halves, low half first.

const IV = new Uint32Array([
  0xf3bcc908, 0x6a09e667, 0x84caa73b, 0xbb67ae85, 0xfe94f82b, 0x3c6ef372, 0x5f1d36f1, 0xa54ff53a,
  0xade682d1, 0x510e527f, 0x2b3e6c1f, 0x9b05688c, 0xfb41bd6b, 0x1f83d9ab, 0x137e2179, 0x5be0cd19,
]);

// The message schedule per round, as offsets into the 32-bit message words.
const SIGMA = new Uint8Array(
  [
    [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15],
    [14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3],
    [11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4],
    [7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8],
    [9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13],
    [2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9],
    [12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11],
    [13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10],
    [6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5],
    [10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0],
    [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15],
    [14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3],
  ]
    .flat()
    .map((i) => i * 2),
);

export class Blake2b512 {
  private readonly h = IV.slice();
  private readonly v = new Uint32Array(32);
  private readonly m = new Uint32Array(32);
  private readonly block = new Uint8Array(128);
  private filled = 0;
  private total = 0;

  constructor() {
    this.h[0] = (this.h[0] ?? 0) ^ 0x01010040;
  }

  update(data: Uint8Array): this {
    let at = 0;
    while (at < data.length) {
      // The last block is compressed in digest(), with the final flag.
      if (this.filled === 128) {
        this.total += 128;
        this.compress(false);
        this.filled = 0;
      }
      const take = Math.min(128 - this.filled, data.length - at);
      this.block.set(data.subarray(at, at + take), this.filled);
      this.filled += take;
      at += take;
    }
    return this;
  }

  digest(): Buffer {
    this.total += this.filled;
    this.block.fill(0, this.filled);
    this.compress(true);
    const out = Buffer.alloc(64);
    for (let i = 0; i < 16; i++) out.writeUInt32LE(this.h[i] ?? 0, i * 4);
    return out;
  }

  private add(a: number, b: number): void {
    const v = this.v;
    const low = (v[a] ?? 0) + (v[b] ?? 0);
    v[a] = low;
    v[a + 1] = (v[a + 1] ?? 0) + (v[b + 1] ?? 0) + (low >= 0x100000000 ? 1 : 0);
  }

  private addWord(a: number, low: number, high: number): void {
    const v = this.v;
    const sum = (v[a] ?? 0) + low;
    v[a] = sum;
    v[a + 1] = (v[a + 1] ?? 0) + high + (sum >= 0x100000000 ? 1 : 0);
  }

  private mix(a: number, b: number, c: number, d: number, x: number, y: number): void {
    const v = this.v;
    const m = this.m;
    this.add(a, b);
    this.addWord(a, m[x] ?? 0, m[x + 1] ?? 0);
    let low = (v[d] ?? 0) ^ (v[a] ?? 0);
    let high = (v[d + 1] ?? 0) ^ (v[a + 1] ?? 0);
    v[d] = high;
    v[d + 1] = low;
    this.add(c, d);
    low = (v[b] ?? 0) ^ (v[c] ?? 0);
    high = (v[b + 1] ?? 0) ^ (v[c + 1] ?? 0);
    v[b] = (low >>> 24) ^ (high << 8);
    v[b + 1] = (high >>> 24) ^ (low << 8);
    this.add(a, b);
    this.addWord(a, m[y] ?? 0, m[y + 1] ?? 0);
    low = (v[d] ?? 0) ^ (v[a] ?? 0);
    high = (v[d + 1] ?? 0) ^ (v[a + 1] ?? 0);
    v[d] = (low >>> 16) ^ (high << 16);
    v[d + 1] = (high >>> 16) ^ (low << 16);
    this.add(c, d);
    low = (v[b] ?? 0) ^ (v[c] ?? 0);
    high = (v[b + 1] ?? 0) ^ (v[c + 1] ?? 0);
    v[b] = (high >>> 31) ^ (low << 1);
    v[b + 1] = (low >>> 31) ^ (high << 1);
  }

  private compress(last: boolean): void {
    const v = this.v;
    v.set(this.h, 0);
    v.set(IV, 16);
    v[24] = (v[24] ?? 0) ^ this.total;
    v[25] = (v[25] ?? 0) ^ Math.floor(this.total / 0x100000000);
    if (last) {
      v[28] = ~(v[28] ?? 0);
      v[29] = ~(v[29] ?? 0);
    }
    const block = this.block;
    for (let i = 0; i < 32; i++) {
      const at = i * 4;
      this.m[i] =
        (block[at] ?? 0) |
        ((block[at + 1] ?? 0) << 8) |
        ((block[at + 2] ?? 0) << 16) |
        ((block[at + 3] ?? 0) << 24);
    }
    for (let round = 0; round < 12; round++) {
      const s = (i: number) => SIGMA[round * 16 + i] ?? 0;
      this.mix(0, 8, 16, 24, s(0), s(1));
      this.mix(2, 10, 18, 26, s(2), s(3));
      this.mix(4, 12, 20, 28, s(4), s(5));
      this.mix(6, 14, 22, 30, s(6), s(7));
      this.mix(0, 10, 20, 30, s(8), s(9));
      this.mix(2, 12, 22, 24, s(10), s(11));
      this.mix(4, 14, 16, 26, s(12), s(13));
      this.mix(6, 8, 18, 28, s(14), s(15));
    }
    for (let i = 0; i < 16; i++) {
      this.h[i] = (this.h[i] ?? 0) ^ (v[i] ?? 0) ^ (v[i + 16] ?? 0);
    }
  }
}

export function blake2b512(data: Uint8Array): Buffer {
  return new Blake2b512().update(data).digest();
}
