import { createHash, randomBytes } from "node:crypto";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import {
  blake2b512,
  Blake2b512,
  MinisignError,
  parsePublicKey,
  parseReleaseSignature,
  parseSignature,
  verifyData,
  verifyFile,
} from "./minisign.ts";
import { signatureFile, testKey } from "./testdata/minisign-key.ts";
import { RELEASE_PUBLIC_KEY } from "./updater.ts";

const COMMENT = "timestamp:1700000000\tfile:baum.AppImage\tversion:1.2.3";
const DATA = Buffer.from("the bundle");

function rejects(run: () => void, message: RegExp): void {
  expect(run).toThrow(MinisignError);
  expect(run).toThrow(message);
}

describe("minisign", () => {
  it("accepts a prehashed signature made for the version", () => {
    const key = testKey();
    const signature = parseSignature(signatureFile(key, DATA, COMMENT));
    expect(signature.algorithm).toBe("ED");
    expect(signature.trustedComment).toBe(COMMENT);
    verifyData(DATA, signature, parsePublicKey(key.publicFile), "1.2.3");
  });

  it("accepts a legacy signature over the file itself", () => {
    const key = testKey();
    const signature = parseSignature(signatureFile(key, DATA, COMMENT, "Ed"));
    expect(signature.algorithm).toBe("Ed");
    verifyData(DATA, signature, parsePublicKey(key.publicFile), "1.2.3");
    rejects(
      () =>
        verifyData(Buffer.from("the bundlE"), signature, parsePublicKey(key.publicFile), "1.2.3"),
      /does not match the file/,
    );
  });

  it("rejects a tampered file", () => {
    const key = testKey();
    const signature = parseSignature(signatureFile(key, DATA, COMMENT));
    rejects(
      () =>
        verifyData(Buffer.from("the bundle!"), signature, parsePublicKey(key.publicFile), "1.2.3"),
      /does not match the file/,
    );
  });

  it("rejects another key, with another or the same key ID", () => {
    const signer = testKey();
    const signature = parseSignature(signatureFile(signer, DATA, COMMENT));
    rejects(
      () => verifyData(DATA, signature, parsePublicKey(testKey().publicFile), "1.2.3"),
      /not the release key/,
    );
    rejects(
      () => verifyData(DATA, signature, parsePublicKey(testKey(signer.id).publicFile), "1.2.3"),
      /trusted comment is not signed/,
    );
  });

  it("rejects a signature made for another version", () => {
    const key = testKey();
    const publicKey = parsePublicKey(key.publicFile);
    const signature = parseSignature(signatureFile(key, DATA, COMMENT));
    for (const version of ["1.2.4", "1.2", "1.2.30", "v1.2.3", ""]) {
      rejects(() => verifyData(DATA, signature, publicKey, version), /not version/);
    }
    for (const comment of ["timestamp:1\tfile:x", "version:1.2.3-rc.1", "xversion:1.2.3"]) {
      const other = parseSignature(signatureFile(key, DATA, comment));
      rejects(() => verifyData(DATA, other, publicKey, "1.2.3"), /not version/);
    }
  });

  it("rejects a trusted comment its global signature does not cover", () => {
    const key = testKey();
    const publicKey = parsePublicKey(key.publicFile);
    const file = signatureFile(key, DATA, "timestamp:1\tversion:1.2.2");
    // The version in the comment is changed after signing.
    const edited = parseSignature(file.replace("version:1.2.2", "version:1.2.3"));
    rejects(() => verifyData(DATA, edited, publicKey, "1.2.3"), /trusted comment is not signed/);
    const flipped = parseSignature(signatureFile(key, DATA, COMMENT));
    flipped.globalSignature[0] = (flipped.globalSignature[0] ?? 0) ^ 1;
    rejects(() => verifyData(DATA, flipped, publicKey, "1.2.3"), /trusted comment is not signed/);
  });

  it("refuses malformed keys and signatures", () => {
    const key = testKey();
    const good = signatureFile(key, DATA, COMMENT);
    expect(() => parsePublicKey("untrusted comment: x\nnot base64!\n")).toThrow(MinisignError);
    expect(() => parsePublicKey(Buffer.alloc(42).toString("base64"))).toThrow(/not an Ed25519/);
    expect(() => parseSignature(good.replace("trusted comment: ", "comment: "))).toThrow(
      MinisignError,
    );
    const lines = good.split("\n");
    const line = Buffer.from(lines[1] ?? "", "base64");
    line.write("XX", 0, "latin1");
    lines[1] = line.toString("base64");
    expect(() => parseSignature(lines.join("\n"))).toThrow(/unknown minisign signature algorithm/);
    expect(() => parseSignature(good.split("\n").slice(0, 3).join("\n"))).toThrow(MinisignError);
    expect(() => parseReleaseSignature("not base64!")).toThrow(MinisignError);
  });

  it("reads a release .sig, the base64 of the signature file", () => {
    const key = testKey();
    const file = signatureFile(key, DATA, COMMENT);
    const signature = parseReleaseSignature(Buffer.from(file).toString("base64"));
    verifyData(DATA, signature, parsePublicKey(key.publicFile), "1.2.3");
    // CRLF line ends and a trailing newline are tolerated.
    const crlf = Buffer.from(file.replaceAll("\n", "\r\n")).toString("base64");
    verifyData(DATA, parseReleaseSignature(`${crlf}\n`), parsePublicKey(key.publicFile), "1.2.3");
  });

  it("verifies files, streaming prehashed ones", async () => {
    const key = testKey();
    const publicKey = parsePublicKey(key.publicFile);
    const dir = mkdtempSync(join(tmpdir(), "baum-minisign-"));
    const path = join(dir, "bundle");
    const data = randomBytes(3 * 65536 + 17);
    writeFileSync(path, data);
    for (const algorithm of ["ED", "Ed"]) {
      const signature = parseSignature(signatureFile(key, data, COMMENT, algorithm));
      await verifyFile(path, signature, publicKey, "1.2.3");
      await expect(verifyFile(path, signature, publicKey, "1.2.4")).rejects.toThrow(/not version/);
    }
    data[100] = (data[100] ?? 0) ^ 1;
    writeFileSync(path, data);
    const signature = parseSignature(signatureFile(key, randomBytes(10), COMMENT));
    await expect(verifyFile(path, signature, publicKey, "1.2.3")).rejects.toThrow(
      /does not match the file/,
    );
  });

  // Made by minisign 0.12 in the desktop-test image (minisign -G -W, then
  // -S and -S -l with updater-sign.sh's trusted comment), and encoded as
  // updater-sign.sh encodes .sig files.
  it("verifies signatures made by minisign", async () => {
    const dir = join(import.meta.dirname, "testdata");
    const key = parsePublicKey(readFileSync(join(dir, "minisign.pub"), "utf8"));
    const payload = join(dir, "minisign-payload.bin");
    for (const name of ["minisign-payload.bin.sig", "minisign-payload-legacy.bin.sig"]) {
      const signature = parseReleaseSignature(readFileSync(join(dir, name), "utf8"));
      expect(signature.algorithm).toBe(name.includes("legacy") ? "Ed" : "ED");
      expect(signature.trustedComment).toBe(
        "timestamp:1700000000\tfile:minisign-payload.bin\tversion:1.2.3",
      );
      await verifyFile(payload, signature, key, "1.2.3");
      expect(() => verifyData(Buffer.from("tampered"), signature, key, "1.2.3")).toThrow(
        /does not match the file/,
      );
      expect(() => verifyData(readFileSync(payload), signature, key, "1.2.4")).toThrow(
        /not version/,
      );
      expect(() =>
        verifyData(readFileSync(payload), signature, parsePublicKey(testKey().publicFile), "1.2.3"),
      ).toThrow(/not the release key/);
    }
  });

  it("the release key rejects another key's signature", () => {
    const release = parsePublicKey(Buffer.from(RELEASE_PUBLIC_KEY, "base64").toString("utf8"));
    expect(Buffer.from(release.keyId).reverse().toString("hex").toUpperCase()).toBe(
      "C42ACF774FCC1D5F",
    );
    // Made with a throwaway key (minisign -G -W) over "baum", with the
    // trusted comment the Tauri CLI writes.
    const signature = parseSignature(OTHER_KEY_SIGNATURE);
    expect(() => verifyData(Buffer.from("baum"), signature, release, "1.2.3")).toThrow(
      MinisignError,
    );
    const other = parsePublicKey(OTHER_PUBLIC_KEY);
    verifyData(Buffer.from("baum"), signature, other, "1.2.3");
    expect(() => verifyData(Buffer.from("baum!"), signature, other, "1.2.3")).toThrow(
      MinisignError,
    );
  });
});

describe("BLAKE2b-512", () => {
  it("matches the RFC 7693 test vector", () => {
    expect(blake2b512(Buffer.from("abc")).toString("hex")).toBe(
      "ba80a53f981c4d0d6a2797b69f12f6e94c212f14685ac4b74b12bb6fdbffa2d1" +
        "7d87c5392aab792dc252d5de4533cc9518d38aa8dbf1925ab92386edd4009923",
    );
  });

  it("matches OpenSSL's for any length and chunking", () => {
    for (const length of [0, 1, 63, 64, 127, 128, 129, 255, 256, 257, 1000, 100_003]) {
      const data = randomBytes(length);
      const expected = createHash("blake2b512").update(data).digest("hex");
      expect(blake2b512(data).toString("hex"), `${length}`).toBe(expected);
      const hash = new Blake2b512();
      for (let at = 0; at < length; at += 77) hash.update(data.subarray(at, at + 77));
      expect(hash.digest().toString("hex"), `${length} in chunks`).toBe(expected);
    }
  });
});

const OTHER_PUBLIC_KEY = "RWTRmLxRd4UKBlQP1/n6WFgBJhCIKQrMRRY1gZi/l8EeH9Gd/co+PB2B";
const OTHER_KEY_SIGNATURE = `untrusted comment: signature from minisign secret key
RUTRmLxRd4UKBnkkf1WYNkfDOLAH8Hil/0iR7CW34Keef2FTYexIM7i6PlNjbcaGQUCR5YcZgAm35/N4KhCffSZru1QQv33Gsg0=
trusted comment: timestamp:1700000000\tfile:m\tversion:1.2.3
vxRKTnoBag5ASJP3FT8bzyzjfEb0wpQE3kaTJ3+e4PEZVtcx3e1WSct0M18bgZDD1yMTQl9oyee+cW1+L8FNDw==
`;
