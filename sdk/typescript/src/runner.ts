import * as readline from 'readline';
import { Event, ControlRequested, ControlResponse } from './pitot';

export type ConsumerHandler = (event: Event) => void | Promise<void>;

export function runConsumer(handler: ConsumerHandler): void {
    const rl = readline.createInterface({
        input: process.stdin,
        output: process.stdout,
        terminal: false
    });

    rl.on('line', async (line) => {
        if (!line.trim()) return;
        try {
            const event = JSON.parse(line) as Event;
            await handler(event);
        } catch (err) {
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
    const rl = readline.createInterface({
        input: process.stdin,
        output: process.stdout,
        terminal: false
    });

    rl.on('line', async (line) => {
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
            
            console.log(JSON.stringify(response));
        } catch (err) {
            console.error("Pitot Controller error:", err);
        }
    });
}
