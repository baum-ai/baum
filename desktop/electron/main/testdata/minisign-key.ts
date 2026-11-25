// Tests only: throwaway minisign keys and signatures made with Node's crypto.
import { createHash, generateKeyPairSync, randomBytes, sign, type KeyObject } from "node:crypto";

export interface TestKey {
  id: Buffer;
  /** The minisign public key file. */
  publicFile: string;
  privateKey: KeyObject;
}

export function testKey(id: Buffer = randomBytes(8)): TestKey {
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const raw = publicKey.export({ format: "der", type: "spki" }).subarray(12);
  const line = Buffer.concat([Buffer.from("Ed"), id, raw]).toString("base64");
  return { id, publicFile: `untrusted comment: minisign public key\n${line}\n`, privateKey };
}

/** A minisign signature file, as `minisign -S` (or `-S -l` for "Ed") writes it. */
export function signatureFile(
  key: TestKey,
  data: Uint8Array,
  comment: string,
  algorithm = "ED",
): string {
  const signed = algorithm === "ED" ? createHash("blake2b512").update(data).digest() : data;
  const signature = sign(null, signed, key.privateKey);
  const global = sign(null, Buffer.concat([signature, Buffer.from(comment)]), key.privateKey);
  const line = Buffer.concat([Buffer.from(algorithm), key.id, signature]).toString("base64");
  return `untrusted comment: signature from minisign secret key\n${line}\ntrusted comment: ${comment}\n${global.toString("base64")}\n`;
}

/** A release's .sig file, as updater-sign.sh writes it. */
export function releaseSignature(key: TestKey, data: Uint8Array, version: string): string {
  const comment = `timestamp:1700000000\tfile:bundle\tversion:${version}`;
  return Buffer.from(signatureFile(key, data, comment)).toString("base64");
}
