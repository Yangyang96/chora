import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { launchBrowser } from './browser.mjs';

const server = createServer((request, response) => {
  response.setHeader('Content-Type', 'text/html; charset=utf-8');
  response.end(`<!doctype html><meta name="viewport" content="width=device-width"><title>Browser qualification</title>
    <label>Name <input id="name"></label><button id="save">Save</button><p id="result"></p>
    <script>const nameInput=document.querySelector('#name');
    document.querySelector('#result').textContent=localStorage.getItem('name')||'';
    document.querySelector('#save').onclick=()=>{localStorage.setItem('name',nameInput.value);document.querySelector('#result').textContent=nameInput.value};</script>`);
});
await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
let browser;
try {
  browser = await launchBrowser();
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  await page.goto(`http://127.0.0.1:${server.address().port}`);
  await page.getByLabel('Name').fill('browser verified');
  await page.getByRole('button', { name: 'Save', exact: true }).focus();
  await page.keyboard.press('Enter');
  assert.equal(await page.locator('#result').textContent(), 'browser verified');
  await page.reload();
  assert.equal(await page.locator('#result').textContent(), 'browser verified');
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  const screenshot = await page.screenshot();
  assert.ok(screenshot.length > 100);
  console.log(JSON.stringify({ schema: 'chora.browser-probe.v1', chromium: browser.version(), passed: true }));
} finally {
  if (browser) await browser.close();
  await new Promise(resolve => server.close(resolve));
}
