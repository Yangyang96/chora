// The browser runs inside Chora's existing Docker boundary. Chromium's nested
// sandbox is unavailable under that boundary; no Docker privileges are added.
import { chromium } from 'playwright-core';
export { chromium };
export async function launchBrowser() {
  return chromium.launch({ headless: true, chromiumSandbox: false });
}
