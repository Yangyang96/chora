// A presentation bridge to the pinned Pi runtime. Pi owns provider discovery,
// login, cancellation and native credential persistence. Never log credentials.
import { createInterface } from 'node:readline';
import { pathToFileURL } from 'node:url';
import path from 'node:path';

export async function authenticate(runtime, interaction) {
  const choices = [];
  for (const provider of runtime.getProviders()) {
    for (const [type, method] of [['oauth', provider.auth.oauth], ['api_key', provider.auth.apiKey]]) {
      if (method?.login) choices.push({ id: `${provider.id}:${type}`, label: method.name, provider: provider.id, type });
    }
  }
  if (!choices.length) throw new Error('No interactive authentication methods');
  const selected = await interaction.prompt({ type: 'select', message: 'Choose a Pi authentication method', options: choices.map(({ id, label }) => ({ id, label })) });
  const choice = choices.find(({ id }) => id === selected);
  if (!choice) throw new Error('Unknown authentication method');
  // Deliberately discard the credential returned by Pi; it must never cross IPC.
  await runtime.login(choice.provider, choice.type, interaction);
}

export function createInteraction(send, signal) {
  let nextID = 0;
  const pending = new Map();
  const prompt = (input) => new Promise((resolve, reject) => {
    const id = ++nextID;
    const signals = [signal, input.signal].filter(Boolean);
    const clean = () => { pending.delete(id); for (const s of signals) s.removeEventListener('abort', cancel); };
    const cancel = () => { clean(); send({ event: 'cancel_prompt', id }); reject(new Error('Authentication cancelled')); };
    if (signals.some(s => s.aborted)) { cancel(); return; }
    pending.set(id, value => { clean(); resolve(value); });
    for (const s of signals) s.addEventListener('abort', cancel, { once: true });
    const { signal: ignored, ...display } = input;
    send({ event: 'prompt', id, ...display });
  });
  return {
    signal, prompt,
    notify(event) { send({ event: 'notice', ...event }); },
    answer(message) { if (typeof message.value === 'string') pending.get(message.id)?.(message.value); },
  };
}

async function main(root) {
  const send = value => process.stdout.write(JSON.stringify(value) + '\n');
  const controller = new AbortController();
  const input = createInterface({ input: process.stdin });
  const interaction = createInteraction(send, controller.signal);
  input.on('line', line => {
    try {
      const message = JSON.parse(line);
      if (message.cancel) controller.abort(); else interaction.answer(message);
    } catch { controller.abort(); }
  });
  input.on('close', () => controller.abort());
  process.on('SIGTERM', () => controller.abort());
  try {
    const { ModelRuntime } = await import(pathToFileURL(path.join(root, 'pi/node_modules/@earendil-works/pi-coding-agent/dist/core/model-runtime.js')).href);
    const runtime = await ModelRuntime.create({ refreshOnCreate: false, allowModelNetwork: false, signal: controller.signal });
    await authenticate(runtime, interaction);
    send({ event: 'complete' });
  } catch {
    // Provider errors can contain secrets/URLs; show a fixed, non-secret message.
    send({ event: controller.signal.aborted ? 'cancelled' : 'failed' });
    process.exitCode = 1;
  } finally {
    input.close();
    process.stdin.destroy();
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) await main(process.argv[2]);
