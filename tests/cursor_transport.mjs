// Cursor's local endpoint accepts HTTP/1 and prior-knowledge HTTP/2 on one port.
// TCP data events need not contain a complete protocol preface.
const PREFACE = Buffer.from("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n");

export function routeClient(
  client,
  { connectBackend, onDecision, onConnect, onClose, onError },
) {
  const prefix = Buffer.alloc(PREFACE.length);
  let length = 0;
  let firstDataBytes;
  let signaturePartialMatch;
  let backend;
  const timer = setTimeout(() => client.destroy(), 5000);
  timer.unref();

  function stopSniffing() {
    clearTimeout(timer);
    client.removeListener("data", sniff);
    client.removeListener("end", incompleteEnd);
  }

  function incompleteEnd() {
    stopSniffing();
    client.destroy();
  }

  client.once("close", () => {
    stopSniffing();
    backend?.destroy();
  });
  client.once("end", incompleteEnd);

  function route(protocol, chunk, consumed) {
    client.pause();
    stopSniffing();
    // Keep following bytes in the socket's own backpressure-managed buffer.
    if (consumed < chunk.length) client.unshift(chunk.subarray(consumed));
    onDecision({
      first_data_bytes: firstDataBytes,
      signature_partial_match: signaturePartialMatch,
      decision_prefix_bytes: length,
      routed_protocol: protocol,
    });
    backend = connectBackend(protocol);
    backend.on("error", (error) => {
      onError(error);
      client.destroy();
    });
    backend.once("close", () => {
      onClose(backend);
      if (!client.destroyed) client.destroySoon();
    });
    backend.once("connect", () => {
      if (client.destroyed) {
        backend.destroy();
        return;
      }
      onConnect(backend);
      // Queue the consumed prefix before pipe can forward further data or EOF.
      backend.write(prefix.subarray(0, length));
      client.pipe(backend).pipe(client);
    });
  }

  function sniff(chunk) {
    if (firstDataBytes === undefined) {
      firstDataBytes = chunk.length;
      const size = Math.min(chunk.length, 14);
      signaturePartialMatch = chunk
        .subarray(0, size)
        .equals(PREFACE.subarray(0, size));
    }
    let consumed = 0;
    while (consumed < chunk.length && length < PREFACE.length) {
      const value = chunk[consumed++];
      prefix[length] = value;
      const matches = value === PREFACE[length++];
      if (!matches) {
        route("http1", chunk, consumed);
        return;
      }
      if (length === PREFACE.length) {
        route("http2", chunk, consumed);
        return;
      }
    }
  }
  client.on("data", sniff);
}
