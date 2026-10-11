import assert from 'node:assert/strict';
import test from 'node:test';

import { isStructuredMetadataRequest } from './mock_anthropic_protocol.js';

test('recognizes Claude structured metadata requests without tools', () => {
  assert.equal(isStructuredMetadataRequest({
    tools: [],
    output_config: { format: { type: 'json_schema' } },
  }), true);
});

test('does not classify the tool-enabled E2E request as metadata', () => {
  assert.equal(isStructuredMetadataRequest({
    tools: [{ name: 'Bash' }],
    output_config: { format: { type: 'json_schema' } },
  }), false);
  assert.equal(isStructuredMetadataRequest({ tools: [] }), false);
});
