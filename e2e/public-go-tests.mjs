import { execFile, spawn } from 'node:child_process'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { createInterface } from 'node:readline'
import { promisify } from 'node:util'

export function planPublicTests(listed, groups) {
  const historical = []
  const seen = new Set()
  for (const group of groups) {
    if (!group.reason || !group.applicability?.startsWith('maintainer_historical_')) throw new Error('invalid historical applicability')
    for (const test of group.tests) {
      if (!/^Test[A-Za-z0-9_]+$/.test(test)) throw new Error(`invalid exact test name: ${test}`)
      const key = `${group.package}:${test}`
      const matches = listed.filter(item => item.test === test && item.package === group.package)
      if (seen.has(key) || matches.length !== 1) {
        throw new Error(`historical test must exist exactly once in its declared package: ${key}`)
      }
      seen.add(key)
      historical.push({ package: group.package, test, reason: group.reason, applicability: group.applicability })
    }
  }
  const patterns = new Map()
  for (const item of historical) {
    const tests = patterns.get(item.package) ?? []
    tests.push(item.test)
    patterns.set(item.package, tests)
  }
  return { historical, skipPatterns: new Map([...patterns].map(([pkg, tests]) => [pkg, `^(${tests.join('|')})$`])) }
}

async function main(packages) {
  if (!packages.length) throw new Error('public package list must not be empty')
  const root = fileURLToPath(new URL('../', import.meta.url))
  const applicability = JSON.parse(await readFile(new URL('./public-test-applicability.json', import.meta.url), 'utf8'))
  const { stdout } = await promisify(execFile)('go', ['test', '-json', '-list', '^(Test|Fuzz|Example)', ...packages], { cwd: root, maxBuffer: 64 * 1024 * 1024 })
  const listed = []
  for (const line of stdout.split('\n').filter(Boolean)) {
    const event = JSON.parse(line)
    const test = event.Output?.trim()
    if (event.Package && /^(Test|Fuzz|Example)[^\s]+$/.test(test ?? '')) listed.push({ package: event.Package, test })
  }
  const plan = planPublicTests(listed, applicability.groups)
  const outcomes = new Map()
  const batches = [{ packages: packages.filter(pkg => !plan.skipPatterns.has(pkg)) }, ...[...plan.skipPatterns].map(([pkg, pattern]) => ({ packages: [pkg], pattern }))]
  let exit = 0
  for (const batch of batches.filter(batch => batch.packages.length)) {
    const command = ['test', '-json', `-p=${process.env.GO_TEST_PACKAGE_PARALLEL ?? '4'}`, `-timeout=${process.env.GO_TEST_TIMEOUT ?? '15m'}`]
    if (batch.pattern) command.push('-skip', batch.pattern)
    command.push(...batch.packages)
    const child = spawn('go', command, { cwd: root, stdio: ['ignore', 'pipe', 'inherit'] })
    const completion = new Promise((resolveExit, reject) => { child.once('error', reject); child.once('close', code => resolveExit(code ?? 1)) })
    for await (const line of createInterface({ input: child.stdout })) {
      const event = JSON.parse(line)
      if (event.Test && !event.Test.includes('/') && ['pass', 'fail', 'skip'].includes(event.Action)) {
        outcomes.set(`${event.Package}:${event.Test}`, { package: event.Package, test: event.Test, outcome: event.Action })
      }
      if (event.Action === 'output') process.stdout.write(event.Output)
    }
    const status = await completion
    if (status !== 0) exit = status
  }
  const report = { scope: 'public source automated checks only', exit, listedTests: listed.length, historicalNotApplicable: plan.historical, outcomes: [...outcomes.values()] }
  await mkdir(resolve(root, 'output'), { recursive: true })
  await writeFile(resolve(root, 'output/public-go-tests.json'), JSON.stringify(report, null, 2) + '\n')
  process.stdout.write(JSON.stringify({ publicGoExit: exit, passed: report.outcomes.filter(item => item.outcome === 'pass').length, failed: report.outcomes.filter(item => item.outcome === 'fail').length, runtimeSkipped: report.outcomes.filter(item => item.outcome === 'skip').length, historicalNotApplicable: plan.historical.length }) + '\n')
  process.exitCode = exit
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main(process.argv.slice(2)).catch(error => { process.stderr.write(error.message + '\n'); process.exitCode = 1 })
}
