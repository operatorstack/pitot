import { TextDecoder } from "node:util";
import { ControlRequested, ControlResponse, Event } from "./pitot";

const MAX_COUNT = 9007199254740991n;
const MAX_DEPTH = 128;
const INVALID = "Pitot: invalid wire envelope";

function reject(): never {
  throw new Error(INVALID);
}

function scalarString(value: unknown): value is string {
  if (typeof value !== "string") return false;
  for (let i = 0; i < value.length; i++) {
    const code = value.charCodeAt(i);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(++i);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
    } else if (code >= 0xdc00 && code <= 0xdfff) return false;
  }
  return true;
}

// Inspect the raw grammar before JSON.parse discards duplicate member names and
// rounds numeric values. Application-owned data is otherwise left untouched.
function inspectJSON(text: string): void {
  let at = 0;
  const whitespace = () => {
    while (/[\x20\t\r\n]/.test(text[at] ?? "") && at < text.length) at++;
  };
  const string = (): string => {
    if (text[at] !== '"') reject();
    const start = at++;
    while (at < text.length) {
      const char = text[at++];
      if (char === '"') {
        const value: unknown = JSON.parse(text.slice(start, at));
        if (!scalarString(value)) reject();
        return value;
      }
      if (char.charCodeAt(0) < 0x20) reject();
      if (char === "\\") {
        const escape = text[at++];
        if (escape === "u") {
          if (!/^[0-9a-fA-F]{4}$/.test(text.slice(at, at + 4))) reject();
          at += 4;
        } else if (!escape || !'"\\/bfnrt'.includes(escape)) reject();
      }
    }
    return reject();
  };
  const value = (path: string[], depth: number): void => {
    whitespace();
    if (text[at] === "{") {
      if (depth >= MAX_DEPTH) reject();
      at++;
      whitespace();
      const seen = new Set<string>();
      if (text[at] === "}") {
        at++;
        return;
      }
      while (true) {
        whitespace();
        const key = string();
        if (seen.has(key)) reject();
        seen.add(key);
        whitespace();
        if (text[at++] !== ":") reject();
        value([...path, key], depth + 1);
        whitespace();
        const end = text[at++];
        if (end === "}") return;
        if (end !== ",") reject();
      }
    }
    if (text[at] === "[") {
      if (depth >= MAX_DEPTH) reject();
      at++;
      whitespace();
      if (text[at] === "]") {
        at++;
        return;
      }
      let index = 0;
      while (true) {
        value([...path, String(index++)], depth + 1);
        whitespace();
        const end = text[at++];
        if (end === "]") return;
        if (end !== ",") reject();
      }
    }
    if (text[at] === '"') {
      string();
      return;
    }
    for (const literal of ["true", "false", "null"]) {
      if (text.startsWith(literal, at)) {
        at += literal.length;
        return;
      }
    }
    const match = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/.exec(
      text.slice(at),
    );
    if (!match) reject();
    const number = match[0];
    if (
      path.length === 2 &&
      path[0] === "usage" &&
      (path[1] === "input_tokens" || path[1] === "output_tokens")
    ) {
      if (
        number.length > 16 ||
        !/^(?:-?0|[1-9][0-9]*)$/.test(number) ||
        BigInt(number) > MAX_COUNT
      )
        reject();
    }
    at += number.length;
  };
  value([], 0);
  whitespace();
  if (at !== text.length) reject();
}

type ObjectValue = Record<string, unknown>;

function object(
  value: unknown,
  required: string[],
  optional: string[] = [],
): ObjectValue {
  if (value === null || typeof value !== "object" || Array.isArray(value))
    reject();
  const record = value as ObjectValue;
  if (required.some((key) => !Object.hasOwn(record, key))) reject();
  const keys = new Set([...required, ...optional]);
  if (Object.keys(record).some((key) => !keys.has(key))) reject();
  return record;
}

function nonempty(value: unknown): void {
  if (!scalarString(value) || value.length === 0) reject();
}

function optionalStrings(record: ObjectValue, keys: string[]): void {
  for (const key of keys) {
    if (Object.hasOwn(record, key) && !scalarString(record[key])) reject();
  }
}

function base(record: ObjectValue, type: string): void {
  if (record.pitot_version !== "1" || record.type !== type) reject();
}

export function validateRequest(
  value: unknown,
): asserts value is ControlRequested {
  const record = object(
    value,
    ["pitot_version", "type", "kind", "action_id"],
    ["data"],
  );
  base(record, "control.requested");
  nonempty(record.kind);
  nonempty(record.action_id);
}

export function validateResponse(
  value: unknown,
): asserts value is ControlResponse {
  const record = object(
    value,
    ["pitot_version", "type", "controller_id", "action_id", "outcome"],
    ["message"],
  );
  base(record, "control.response");
  nonempty(record.controller_id);
  nonempty(record.action_id);
  if (record.outcome !== "allow" && record.outcome !== "deny") reject();
  optionalStrings(record, ["message"]);
}

export function validateEvent(value: unknown): asserts value is Event {
  const record = object(
    value,
    ["pitot_version", "type", "host", "observation"],
    ["id", "time", "session_id", "action", "content", "usage"],
  );
  if (
    record.pitot_version !== "1" ||
    !["action.requested", "model.usage"].includes(record.type as string)
  )
    reject();
  optionalStrings(record, ["id", "time", "session_id"]);
  const host = object(record.host, ["name"], ["adapter_version"]);
  nonempty(host.name);
  optionalStrings(host, ["adapter_version"]);
  const observation = object(record.observation, ["source", "fidelity"]);
  if (
    !["host_hook", "host_event"].includes(observation.source as string) ||
    !["direct", "estimated"].includes(observation.fidelity as string)
  )
    reject();
  if (Object.hasOwn(record, "action")) {
    const action = object(record.action, ["id", "kind"]);
    nonempty(action.id);
    nonempty(action.kind);
  }
  if (Object.hasOwn(record, "usage")) {
    const usage = object(record.usage, ["input_tokens", "output_tokens"]);
    for (const key of ["input_tokens", "output_tokens"]) {
      const count = usage[key];
      if (
        typeof count !== "number" ||
        !Number.isSafeInteger(count) ||
        count < 0
      )
        reject();
    }
  }
  if (record.type === "action.requested" && !Object.hasOwn(record, "action"))
    reject();
  if (record.type === "model.usage" && !Object.hasOwn(record, "usage"))
    reject();
  if (Object.hasOwn(record, "content")) {
    const content = object(record.content, ["mode"], ["full", "sha256"]);
    switch (content.mode) {
      case "full":
        if (
          !Object.hasOwn(content, "full") ||
          !scalarString(content.full) ||
          Object.hasOwn(content, "sha256")
        )
          reject();
        break;
      case "sha256":
        if (
          typeof content.sha256 !== "string" ||
          content.sha256.length !== 64 ||
          !/^[0-9a-fA-F]{64}$/.test(content.sha256) ||
          Object.hasOwn(content, "full")
        )
          reject();
        break;
      case "omit":
        if (Object.hasOwn(content, "full") || Object.hasOwn(content, "sha256"))
          reject();
        break;
      default:
        reject();
    }
  }
}

export function decodeWire(bytes: Uint8Array): unknown {
  const text = new TextDecoder("utf-8", {
    fatal: true,
    ignoreBOM: true,
  }).decode(bytes);
  inspectJSON(text);
  return JSON.parse(text);
}
