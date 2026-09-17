import test from 'node:test';
import assert from 'node:assert/strict';
import { authenticate, createInteraction } from './auth.mjs';

test('provider-owned login persists credentials without returning them to the UI', async () => {
  const events = [];
  const abort = new AbortController();
  const interaction = createInteraction(e => {
    events.push(e);
    if (e.event === 'prompt') queueMicrotask(() => interaction.answer({ id: e.id, value: e.type === 'select' ? 'custom:api_key' : 'fixture-secret' }));
  }, abort.signal);
  let selected;
  await authenticate({
    getProviders: () => [{ id: 'custom', auth: { apiKey: { name: 'Custom API key', login() {} } } }],
    async login(provider, type, callbacks) {
      selected = [provider, type];
      assert.equal(await callbacks.prompt({ type: 'secret', message: 'API key' }), 'fixture-secret');
      return { type: 'api_key', key: 'fixture-secret' };
    },
  }, interaction);
  assert.deepEqual(selected, ['custom', 'api_key']);
  assert.ok(!JSON.stringify(events).includes('fixture-secret'));
});

test('OAuth callback cancels only its outstanding manual prompt', async () => {
  const events = [];
  const global = new AbortController();
  const prompt = new AbortController();
  const interaction = createInteraction(e => events.push(e), global.signal);
  const pending = interaction.prompt({ type: 'manual_code', message: 'Code', signal: prompt.signal });
  prompt.abort();
  await assert.rejects(pending, /cancelled/);
  assert.equal(global.signal.aborted, false);
  assert.equal(events.at(-1).event, 'cancel_prompt');
  assert.ok(!JSON.stringify(events).includes('signal'));
});

test('global cancellation rejects outstanding prompts and unknown selections never log in', async () => {
  const abort = new AbortController();
  const interaction = createInteraction(() => {}, abort.signal);
  const pending = interaction.prompt({ type: 'secret', message: 'Key' });
  abort.abort();
  await assert.rejects(pending, /cancelled/);
  await assert.rejects(authenticate({ getProviders: () => [] }, interaction), /No interactive/);
});
