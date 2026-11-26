import { readFileSync } from "node:fs";
import http from "node:http";
import type { AddressInfo } from "node:net";
import { Hansho, type HanshoOptions } from "../src/index.ts";

export const TEST_KEY = "ck_test_SECRET_do_not_leak";

export interface Recorded {
  method: string;
  /** The raw request target, exactly as sent. */
  url: string;
  headers: http.IncomingHttpHeaders;
  body: Buffer;
}

export interface Reply {
  status: number;
  headers: Record<string, string>;
  body: string;
}

export interface Hub {
  url: string;
  requests: Recorded[];
  waits: number[];
  client: Hansho;
  newClient(opts?: Partial<HanshoOptions>): Hansho;
  close(): Promise<void>;
}

/** A local stand-in for the hub. `fn` fills the reply (default 200 {}). */
export async function startHub(fn?: (r: Recorded, reply: Reply) => void, opts: Partial<HanshoOptions> = {}): Promise<Hub> {
  const requests: Recorded[] = [];
  const waits: number[] = [];
  const server = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on("data", (c: Buffer) => chunks.push(c));
    req.on("end", () => {
      const rec: Recorded = { method: req.method ?? "", url: req.url ?? "", headers: req.headers, body: Buffer.concat(chunks) };
      requests.push(rec);
      const reply: Reply = { status: 200, headers: {}, body: "{}" };
      fn?.(rec, reply);
      res.writeHead(reply.status, { "content-type": "application/json", ...reply.headers });
      res.end(reply.status === 204 ? undefined : reply.body);
    });
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  const newClient = (o: Partial<HanshoOptions> = {}) =>
    new Hansho({
      baseUrl: url,
      apiKey: TEST_KEY,
      sleep: async (ms, signal) => {
        waits.push(ms);
        signal?.throwIfAborted();
      },
      ...opts,
      ...o,
    });
  return {
    url,
    requests,
    waits,
    client: newClient(),
    newClient,
    close: () =>
      new Promise<void>((resolve) => {
        server.closeAllConnections();
        server.close(() => resolve());
      }),
  };
}

export interface SpecOp {
  name: string;
  /** The operationId. */
  operation: string;
  method: string;
  path: string;
  request: null | { content_type: string; body?: unknown; raw_base64?: string };
  response: { status: number; body: unknown };
}

export interface SpecErr {
  status: number;
  headers: Record<string, string>;
  body: unknown;
  raw?: string;
  code: string;
  field: string;
  retry_after_ms: number;
}

/** The conformance cases of the shared contract: the requests every SDK must produce and the answers the hub gives. */
export function loadSpec(): { operations: SpecOp[]; errors: SpecErr[] } {
  return JSON.parse(readFileSync(new URL("../../spec/openapi.json", import.meta.url), "utf8"))["x-conformance"];
}

export function loadJson<T>(rel: string): T {
  return JSON.parse(readFileSync(new URL(rel, import.meta.url), "utf8")) as T;
}
