import { protoInt64 } from "@bufbuild/protobuf";

export function uint64ToString(value: unknown): string {
  try {
    return protoInt64.parse(value).toString();
  } catch {
    return "";
  }
}

export async function readFileBytes(file: File): Promise<Uint8Array> {
  const buffer = await file.arrayBuffer();
  return new Uint8Array(buffer);
}

export function escHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}
