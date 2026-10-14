// Hand-written. The package's public surface: everything oag generated, and
// the thin layer over it in collegium.ts.
export * from "./generated/index.js";
export type { SSEEvent } from "./generated/sse.js";
export {
  Collegium,
  createClient,
  decodeEvent,
  endsRun,
  failureOf,
  isRefusal,
  topicFor,
  type ApiFailure,
  type CollegiumOptions,
  type DecodedRunEvent,
  type StreamItem,
  type TokenSource,
} from "./collegium.js";
