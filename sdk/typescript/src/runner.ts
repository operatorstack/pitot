import { Event, ControlRequested, ControlResponse } from "./pitot";
import {
  decodeWire,
  validateEvent,
  validateRequest,
  validateResponse,
} from "./wire";

export type ConsumerHandler = (event: Event) => void | Promise<void>;

const MAX_FRAME = 4 * 1024 * 1024;

// Awaiting each handler preserves FIFO and applies stream backpressure rather
// than accumulating a promise for every incoming line. Keep bytes intact until
// strict UTF-8 decoding; readline would replace malformed byte sequences.
function serializeLines(onLine: (line: Buffer) => Promise<void>): void {
  void (async () => {
    let pieces: Buffer[] = [];
    let length = 0;
    let oversized = false;
    const append = (part: Buffer) => {
      if (oversized) return;
      length += part.length;
      if (length > MAX_FRAME) {
        pieces = [];
        oversized = true;
      } else pieces.push(part);
    };
    const finish = async () => {
      if (oversized) console.error("Pitot: invalid wire frame");
      else if (length) await onLine(Buffer.concat(pieces, length));
      pieces = [];
      length = 0;
      oversized = false;
    };
    try {
      for await (const chunk of process.stdin) {
        if (!Buffer.isBuffer(chunk)) throw new Error("invalid input stream");
        let start = 0;
        let newline: number;
        while ((newline = chunk.indexOf(10, start)) !== -1) {
          append(chunk.subarray(start, newline));
          await finish();
          start = newline + 1;
        }
        append(chunk.subarray(start));
      }
      if (length || oversized) await finish();
    } catch {
      console.error("Pitot: input stream error");
    }
  })();
}

function blank(line: Buffer): boolean {
  return line.every(
    (byte) => byte === 32 || byte === 9 || byte === 13 || byte === 10,
  );
}

export function runConsumer(handler: ConsumerHandler): void {
  serializeLines(async (line) => {
    if (blank(line)) return;
    try {
      const event = decodeWire(line);
      validateEvent(event);
      await handler(event);
    } catch {
      // Malformed input and handler faults are reported and skipped; the
      // stream continues so one bad line never tears down the runner.
      console.error("Pitot Consumer error: invalid envelope or handler fault");
    }
  });
}

export type Outcome = { outcome: "allow" | "deny"; message?: string };

export function allow(message?: string): Outcome {
  return { outcome: "allow", message };
}

export function deny(message?: string): Outcome {
  return { outcome: "deny", message };
}

export type ControllerHandler = (
  req: ControlRequested,
) => Outcome | Promise<Outcome>;

export function runController(
  controllerId: string,
  handler: ControllerHandler,
): void {
  let outputUnavailable = false;
  class OutputFault extends Error {}
  const failOutput = () => {
    if (!outputUnavailable) {
      console.error("Pitot Controller error: output unavailable");
      outputUnavailable = true;
    }
  };
  // Stream error events can arrive after the write callback. Keep this listener
  // for the controller's lifetime rather than removing it when a write settles.
  process.stdout.on("error", failOutput);
  const writeResponse = (encoded: string): Promise<void> =>
    new Promise((resolve, reject) => {
      try {
        process.stdout.write(encoded + "\n", (error) => {
          if (error) {
            failOutput();
            reject(new OutputFault());
          } else resolve();
        });
      } catch {
        failOutput();
        reject(new OutputFault());
      }
    });
  serializeLines(async (line) => {
    if (outputUnavailable || blank(line)) return;
    try {
      const req = decodeWire(line);
      validateRequest(req);
      const result = await handler(req);

      const response: ControlResponse = {
        pitot_version: "1",
        type: "control.response",
        controller_id: controllerId,
        action_id: req.action_id,
        outcome: result.outcome,
      };
      if (result.message !== undefined) {
        response.message = result.message;
      }
      validateResponse(response);

      // Exactly one response per successfully parsed request, in order.
      const encoded = JSON.stringify(response);
      if (Buffer.byteLength(encoded, "utf8") > MAX_FRAME) {
        throw new Error("invalid output frame");
      }
      await writeResponse(encoded);
    } catch (error) {
      if (error instanceof OutputFault) return;
      // Malformed input and handler faults are reported and skipped; no
      // response line is emitted for an unparseable request.
      console.error(
        "Pitot Controller error: invalid envelope or handler fault",
      );
    }
  });
}
