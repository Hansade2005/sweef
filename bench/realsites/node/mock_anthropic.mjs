#!/usr/bin/env node
// TEST-ONLY mock of the Anthropic Messages API, used to verify Mode B plumbing
// (request shapes, SSE parsing, tool loop, result recording, key handling).
// It is scripted: turn 1 asks to read the page, turn 2 calls done with MOCK_ANSWER.
// Numbers produced against it are meaningless and must never be reported as results.
import http from 'node:http';

const port = +(process.argv[2] || 0);
const answer = process.env.MOCK_ANSWER || 'mock answer';
let reqs = 0;
const seenKeys = new Set();

function script(body) {
  const names = new Set((body.tools || []).map((t) => t.name));
  const lastUser = body.messages.at(-1);
  const hasResult = Array.isArray(lastUser.content) && lastUser.content.some((b) => b.type === 'tool_result');
  const sweefStyle = names.has('act');
  if (!hasResult) {
    return sweefStyle
      ? { id: 'toolu_mock1', name: 'read_page', input: {} }
      : { id: 'toolu_mock1', name: 'snapshot', input: {} };
  }
  return sweefStyle
    ? { id: 'toolu_mock2', name: 'done', input: { summary: answer, success: true } }
    : { id: 'toolu_mock2', name: 'done', input: { answer, success: true } };
}

const srv = http.createServer((req, res) => {
  let b = '';
  req.on('data', (d) => (b += d));
  req.on('end', () => {
    if (req.url === '/__stats') { res.end(JSON.stringify({ reqs, keys: seenKeys.size })); return; }
    if (req.method !== 'POST' || !req.url.startsWith('/v1/messages')) { res.statusCode = 404; res.end('{}'); return; }
    reqs++;
    seenKeys.add(req.headers['x-api-key'] ? 'present' : 'missing');
    const body = JSON.parse(b);
    const tu = script(body);
    const usage = { input_tokens: 1200, output_tokens: 40, cache_creation_input_tokens: 0, cache_read_input_tokens: 0 };
    if (body.stream) {
      res.writeHead(200, { 'content-type': 'text/event-stream', 'request-id': 'req_mock' });
      const ev = (n, d) => res.write(`event: ${n}\ndata: ${JSON.stringify(d)}\n\n`);
      ev('message_start', { type: 'message_start', message: { id: 'msg_mock', type: 'message', role: 'assistant', model: body.model, content: [], stop_reason: null, usage: { ...usage, output_tokens: 1 } } });
      ev('content_block_start', { type: 'content_block_start', index: 0, content_block: { type: 'tool_use', id: tu.id, name: tu.name, input: {} } });
      const j = JSON.stringify(tu.input);
      ev('content_block_delta', { type: 'content_block_delta', index: 0, delta: { type: 'input_json_delta', partial_json: j } });
      ev('content_block_stop', { type: 'content_block_stop', index: 0 });
      ev('message_delta', { type: 'message_delta', delta: { stop_reason: 'tool_use', stop_sequence: null }, usage: { output_tokens: usage.output_tokens } });
      ev('message_stop', { type: 'message_stop' });
      res.end();
    } else {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ id: 'msg_mock', type: 'message', role: 'assistant', model: body.model, content: [{ type: 'tool_use', id: tu.id, name: tu.name, input: tu.input }], stop_reason: 'tool_use', usage }));
    }
  });
});
srv.listen(port, '127.0.0.1', () => console.log(srv.address().port));
