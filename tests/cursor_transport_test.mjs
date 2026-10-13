import assert from "node:assert/strict";
import net from "node:net";
import { once } from "node:events";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";
import { routeClient } from "./cursor_transport.mjs";

const PREFACE = Buffer.from("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n");

async function fixture(t, rejectBackend = false, connectDelay = 0) {
  const sockets = new Set();
  const decisions = [];
  const received = [];
  const errors = [];
  let backendConnections = 0;
  const track = (socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
    socket.on("error", (error) => errors.push(error.code));
    return socket;
  };
  const backend = net.createServer({ allowHalfOpen: true }, (socket) => {
    track(socket);
    if (rejectBackend) {
      socket.destroy();
      return;
    }
    const chunks = [];
    socket.on("data", (chunk) => chunks.push(chunk));
    socket.on("end", () => {
      received.push(Buffer.concat(chunks));
      socket.end("received");
    });
  });
  backend.listen(0, "127.0.0.1");
  await once(backend, "listening");
  const front = net.createServer({ allowHalfOpen: true }, (socket) => {
    track(socket);
    routeClient(socket, {
      connectBackend: () => {
        backendConnections++;
        if (connectDelay) {
          const socket = new net.Socket();
          setTimeout(() => {
            if (!socket.destroyed) {
              socket.connect(backend.address().port, "127.0.0.1");
            }
          }, connectDelay);
          return socket;
        }
        return net.connect(backend.address().port, "127.0.0.1");
      },
      onDecision: (decision) => decisions.push(decision),
      onConnect: () => {},
      onClose: () => {},
      onError: (error) => errors.push(error.code),
    });
  });
  front.listen(0, "127.0.0.1");
  await once(front, "listening");
  t.after(async () => {
    for (const socket of sockets) socket.destroy();
    await Promise.all([
      new Promise((resolve) => front.close(resolve)),
      new Promise((resolve) => backend.close(resolve)),
    ]);
  });
  return {
    decisions,
    received,
    errors,
    count: () => backendConnections,
    connect: async () => {
      const socket = track(net.connect(front.address().port, "127.0.0.1"));
      socket.on("data", () => {});
      await once(socket, "connect");
      return socket;
    },
  };
}

async function send(f, chunks) {
  const socket = await f.connect();
  const closed = once(socket, "close");
  for (const chunk of chunks) {
    socket.write(chunk);
    await delay(5);
  }
  socket.end();
  await closed;
}

test("HTTP2 preface splits and following frames retain exact bytes", async (t) => {
  const f = await fixture(t);
  const body = Buffer.concat([
    PREFACE,
    Buffer.from([0, 0, 0, 4, 0, 0, 0, 0, 0]),
  ]);
  for (let offset = 1; offset < PREFACE.length; offset++) {
    await send(f, [body.subarray(0, offset), body.subarray(offset)]);
    assert.equal(f.decisions.at(-1).routed_protocol, "http2");
    assert.equal(f.decisions.at(-1).decision_prefix_bytes, 24);
    assert.deepEqual(f.received.at(-1), body);
  }
  await send(
    f,
    [...PREFACE].map((byte) => Buffer.from([byte])).concat(body.subarray(24)),
  );
  await send(f, [body]);
  assert.equal(f.decisions.length, 25);
  assert.ok(
    f.decisions.every((decision) => decision.routed_protocol === "http2"),
  );
  assert.ok(f.received.every((bytes) => bytes.equals(body)));
  assert.deepEqual(f.errors, []);
});

test("HTTP1 GET, POST and pipelined body preserve original order", async (t) => {
  const f = await fixture(t);
  const get = Buffer.from("GET /first HTTP/1.1\r\nHost: local\r\n\r\n");
  const post = Buffer.from(
    "POST /second HTTP/1.1\r\nHost: local\r\nContent-Length: 7\r\n\r\nopaque!",
  );
  for (const body of [get, post, Buffer.concat([get, post])]) {
    for (const offset of [1, 2, 7, body.length - 1]) {
      await send(f, [body.subarray(0, offset), body.subarray(offset)]);
      assert.equal(f.decisions.at(-1).routed_protocol, "http1");
      assert.deepEqual(f.received.at(-1), body);
    }
  }
  assert.deepEqual(f.errors, []);
});

test("full preface mismatch routes HTTP1 without inventing HTTP2", async (t) => {
  const f = await fixture(t);
  const body = Buffer.from(PREFACE);
  body[23] ^= 1;
  await send(f, [body.subarray(0, 13), body.subarray(13)]);
  assert.equal(f.decisions[0].routed_protocol, "http1");
  assert.deepEqual(f.received[0], body);
});

test("bytes arriving before backend connection retain order and backpressure", async (t) => {
  const f = await fixture(t, false, 200);
  const body = Buffer.concat([PREFACE, Buffer.alloc(256 * 1024, 0x61)]);
  await send(f, [
    body.subarray(0, 13),
    body.subarray(13, 24),
    body.subarray(24),
  ]);
  assert.equal(f.decisions[0].decision_prefix_bytes, 24);
  assert.deepEqual(f.received[0], body);
  assert.deepEqual(f.errors, []);
});

test("empty and partial EOF close without opening a backend", async (t) => {
  const f = await fixture(t);
  for (let offset = 0; offset < PREFACE.length; offset++) {
    await send(f, [PREFACE.subarray(0, offset)]);
  }
  assert.equal(f.count(), 0);
  assert.equal(f.decisions.length, 0);
  assert.deepEqual(f.errors, []);
});

test("idle and trickled undecided prefixes have an absolute five-second deadline", async (t) => {
  const f = await fixture(t);
  const idle = await f.connect();
  const partial = await f.connect();
  const began = Date.now();
  const closed = Promise.all([once(idle, "close"), once(partial, "close")]);
  partial.write("P");
  await delay(2000);
  partial.write("R");
  await closed;
  const elapsed = Date.now() - began;
  assert.ok(elapsed >= 4500 && elapsed < 9000, `deadline took ${elapsed}ms`);
  assert.equal(f.count(), 0);
  assert.equal(f.decisions.length, 0);
});

test("backend closure closes its owned client", async (t) => {
  const f = await fixture(t, true);
  const socket = await f.connect();
  const closed = once(socket, "close");
  socket.write("GET / HTTP/1.1\r\n\r\n");
  await closed;
  assert.equal(f.count(), 1);
});
