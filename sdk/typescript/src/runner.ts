import * as readline from 'readline';
import { Event, ControlRequested, ControlResponse } from './pitot';

export type ConsumerHandler = (event: Event) => void | Promise<void>;

// serializeLines drives an async per-line handler strictly in arrival order.
// readline does not await listener promises, so without this a slow handler
// could let a later line's response overtake an earlier one. We chain each
// line onto a single promise so handlers run — and responses are emitted —
// in the exact order the lines arrived.
function serializeLines(onLine: (line: string) => Promise<void>): void {
    const rl = readline.createInterface({
        input: process.stdin,
        output: process.stdout,
        terminal: false,
    });

    let chain: Promise<void> = Promise.resolve();
    rl.on('line', (line) => {
        chain = chain.then(() => onLine(line));
    });
}

export function runConsumer(handler: ConsumerHandler): void {
    serializeLines(async (line) => {
        if (!line.trim()) return;
        try {
            const event = JSON.parse(line) as Event;
            await handler(event);
        } catch (err) {
            // Malformed input and handler faults are reported and skipped; the
            // stream continues so one bad line never tears down the runner.
            console.error("Pitot Consumer error:", err);
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

export type ControllerHandler = (req: ControlRequested) => Outcome | Promise<Outcome>;

export function runController(controllerId: string, handler: ControllerHandler): void {
    serializeLines(async (line) => {
        if (!line.trim()) return;
        try {
            const req = JSON.parse(line) as ControlRequested;
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

            // Exactly one response per successfully parsed request, in order.
            console.log(JSON.stringify(response));
        } catch (err) {
            // Malformed input and handler faults are reported and skipped; no
            // response line is emitted for an unparseable request.
            console.error("Pitot Controller error:", err);
        }
    });
}
