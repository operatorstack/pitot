import http from 'http';

import { isStructuredMetadataRequest } from './mock_anthropic_protocol.js';

const PORT = 8080;

const server = http.createServer((req, res) => {
  console.log(`[MOCK API] ${req.method} ${req.url}`);
  // Set CORS headers
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Access-Control-Allow-Headers', '*');
  res.setHeader('Access-Control-Allow-Methods', '*');

  if (req.method === 'OPTIONS') {
    res.writeHead(200);
    res.end();
    return;
  }

  if (req.url.startsWith('/v1/messages') && req.method === 'POST') {
    let body = '';
    req.on('data', chunk => {
      body += chunk;
    });

    req.on('end', () => {
      try {
        console.log(`[MOCK API] Request Body: ${body}`);
        const payload = JSON.parse(body || '{}');
        const messages = payload.messages || [];
        const lastMessage = messages[messages.length - 1] || {};
        const requestedModel = payload.model || 'claude-3-5-sonnet';

        res.setHeader('Content-Type', 'text/event-stream');
        res.setHeader('Cache-Control', 'no-cache');
        res.setHeader('Connection', 'keep-alive');

        if (isStructuredMetadataRequest(payload)) {
          sendTextResponse(res, requestedModel, 'msg_metadata_42', '{"title":"List directory"}');
          return;
        }

        // If the last message contains a tool result, we have successfully run the tool!
        const hasToolResult = lastMessage.content && lastMessage.content.some(c => c.type === 'tool_result');

        if (hasToolResult) {
          // Send a final message saying we're done
          sendSSEEvent(res, 'message_start', {
            type: 'message_start',
            message: { id: 'msg_done_42', type: 'message', role: 'assistant', content: [], model: requestedModel, stop_reason: null, stop_sequence: null, usage: { input_tokens: 100, output_tokens: 1 } }
          });
          sendSSEEvent(res, 'content_block_start', {
            type: 'content_block_start',
            index: 0,
            content_block: { type: 'text', text: '' }
          });
          sendSSEEvent(res, 'content_block_delta', {
            type: 'content_block_delta',
            index: 0,
            delta: { type: 'text_delta', text: 'E2E Verification Complete: Tool executed successfully!' }
          });
          sendSSEEvent(res, 'content_block_stop', { type: 'content_block_stop', index: 0 });
          sendSSEEvent(res, 'message_delta', { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 20 } });
          sendSSEEvent(res, 'message_stop', { type: 'message_stop' });
          res.end();

          // Gracefully shut down the server after a short delay since E2E is complete!
          setTimeout(() => {
            server.close(() => {
              process.exit(0);
            });
          }, 1000);
          return;
        }

        // Instruct Claude Code to execute the Bash tool with 'git status --short'
        sendSSEEvent(res, 'message_start', {
          type: 'message_start',
          message: { id: 'msg_tool_42', type: 'message', role: 'assistant', content: [], model: requestedModel, stop_reason: null, stop_sequence: null, usage: { input_tokens: 50, output_tokens: 1 } }
        });
        sendSSEEvent(res, 'content_block_start', {
          type: 'content_block_start',
          index: 0,
          content_block: { type: 'tool_use', id: 'toolu_e2e_42', name: 'Bash', input: {} }
        });
        sendSSEEvent(res, 'content_block_delta', {
          type: 'content_block_delta',
          index: 0,
          delta: { type: 'input_json_delta', partial_json: '{"command": "git status --short"}' }
        });
        sendSSEEvent(res, 'content_block_stop', { type: 'content_block_stop', index: 0 });
        sendSSEEvent(res, 'message_delta', { type: 'message_delta', delta: { stop_reason: 'tool_use', stop_sequence: null }, usage: { output_tokens: 20 } });
        sendSSEEvent(res, 'message_stop', { type: 'message_stop' });
        res.end();
      } catch (err) {
        console.error(`[MOCK API ERROR] ${err.stack}`);
        res.writeHead(500);
        res.end(JSON.stringify({ error: err.message }));
      }
    });
  } else if (req.url.startsWith('/v1/chat/completions') && req.method === 'POST') {
    let body = '';
    req.on('data', chunk => {
      body += chunk;
    });

    req.on('end', () => {
      try {
        console.log(`[MOCK API] OpenAI Request Body: ${body}`);
        const payload = JSON.parse(body || '{}');
        const messages = payload.messages || [];
        const lastMessage = messages[messages.length - 1] || {};
        const requestedModel = payload.model || 'gpt-4';

        res.setHeader('Content-Type', 'application/json');

        // Check if last message contains tool execution response
        const isToolResult = lastMessage.role === 'tool' || lastMessage.role === 'function';

        if (isToolResult) {
          // Send final OpenAI-compatible text completion response
          res.writeHead(200);
          res.end(JSON.stringify({
            id: "chatcmpl-done-42",
            object: "chat.completion",
            created: 1781881881,
            model: requestedModel,
            choices: [{
              index: 0,
              message: {
                role: "assistant",
                content: "E2E Verification Complete: Tool executed successfully!"
              },
              finish_reason: "stop"
            }],
            usage: { prompt_tokens: 100, completion_tokens: 20, total_tokens: 120 }
          }));

          // Gracefully shut down
          setTimeout(() => {
            server.close(() => {
              process.exit(0);
            });
          }, 1000);
          return;
        }

        // Return a tool_calls completion telling Cursor/Codex to execute git status --short
        res.writeHead(200);
        res.end(JSON.stringify({
          id: "chatcmpl-tool-42",
          object: "chat.completion",
          created: 1781881881,
          model: requestedModel,
          choices: [{
            index: 0,
            message: {
              role: "assistant",
              content: null,
              tool_calls: [{
                id: "call_e2e_42",
                type: "function",
                function: {
                  name: "Bash",
                  arguments: "{\"command\":\"git status --short\"}"
                }
              }]
            },
            finish_reason: "tool_calls"
          }],
          usage: { prompt_tokens: 50, completion_tokens: 20, total_tokens: 70 }
        }));
      } catch (err) {
        console.error(`[MOCK API ERROR] ${err.stack}`);
        res.writeHead(500);
        res.end(JSON.stringify({ error: err.message }));
      }
    });
  } else {
    res.writeHead(404);
    res.end();
  }
});

function sendSSEEvent(res, eventName, data) {
  res.write(`event: ${eventName}\n`);
  res.write(`data: ${JSON.stringify(data)}\n\n`);
}

function sendTextResponse(res, model, messageId, text) {
  sendSSEEvent(res, 'message_start', {
    type: 'message_start',
    message: { id: messageId, type: 'message', role: 'assistant', content: [], model, stop_reason: null, stop_sequence: null, usage: { input_tokens: 10, output_tokens: 1 } }
  });
  sendSSEEvent(res, 'content_block_start', {
    type: 'content_block_start',
    index: 0,
    content_block: { type: 'text', text: '' }
  });
  sendSSEEvent(res, 'content_block_delta', {
    type: 'content_block_delta',
    index: 0,
    delta: { type: 'text_delta', text }
  });
  sendSSEEvent(res, 'content_block_stop', { type: 'content_block_stop', index: 0 });
  sendSSEEvent(res, 'message_delta', { type: 'message_delta', delta: { stop_reason: 'end_turn', stop_sequence: null }, usage: { output_tokens: 10 } });
  sendSSEEvent(res, 'message_stop', { type: 'message_stop' });
  res.end();
}

server.listen(PORT, () => {
  console.log(`Mock Anthropic Server running on http://localhost:${PORT}`);
});
