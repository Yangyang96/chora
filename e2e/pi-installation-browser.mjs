import {chromium, expect} from '@playwright/test'
import {join} from 'node:path'
const [baseURL, output] = process.argv.slice(2)
if (!baseURL || !output) throw Error('missing qualification arguments')
const browser = await chromium.launch({headless:true})
const context = await browser.newContext()
await context.tracing.start({screenshots:true,snapshots:true,sources:true})
try {
 const page = await context.newPage()
 await page.goto(baseURL)
 const panel = page.getByRole('region',{name:'Pi installation',exact:true})
 await expect(panel).toContainText('@earendil-works/pi-coding-agent@0.85.1')
 const installButton = panel.getByRole('button',{name:/^(Install fixed Pi version|Retry installation)$/})
 await expect(installButton).toBeVisible()
 const completed = page.waitForResponse(response => response.url() === baseURL + '/api/pi/installation' && response.request().method() === 'POST', {timeout:650000})
 await installButton.click()
 const response = await completed
 const result = await response.json()
 if (!response.ok() || result.state?.code !== 'installed') throw Error('Real installation failed: '+JSON.stringify(result))
 await expect(panel).toContainText('Pi is installed.')
 await expect(panel).toContainText('/login')
 await expect(panel).toContainText('/model')
 await expect(panel).toContainText('Restart')
 await expect(panel.getByRole('button',{name:'Install fixed Pi version',exact:true})).toHaveCount(0)
 await page.reload()
 await expect(panel).toContainText('Pi is installed.')
 await panel.screenshot({path:join(output,'installed.png')})
 console.log('PI_INSTALLATION_BROWSER_PASS')
} finally {
 await context.tracing.stop({path:join(output,'trace.zip')})
 await browser.close()
}
