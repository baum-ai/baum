const { resolve } = require('node:path');
const { chromium, expect } = require(resolve(__dirname, '../../app/node_modules/@playwright/test'));
const desktop = resolve(__dirname, '..');
const fs = require('node:fs/promises');
const assert = require('node:assert/strict');
(async () => {
  const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] });
  const page = await browser.newPage({ viewport: { width: 1000, height: 800 } });
  const screenshots = process.env.ONBOARDING_SCREENSHOT_DIR || await fs.mkdtemp(resolve(require('node:os').tmpdir(), 'baum-onboarding-'));
  await fs.mkdir(screenshots, { recursive: true });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const calls = [];
  let fail = false;
  const disconnected = { state: 'disconnected', host: 'github.com', saved: false, override: false };
  let github = disconnected;
  const picked = new Set();
  const localClones = new Map([["acme/found", "/workspace/sample/found"]]);
  let cloning = false;
  let reviews = { backend: 'auto', model: '', automatic: true, pull_requests: 'both' };
  const state = { first_run: true, home: '/workspace/sample', command: { installed: false, target: '/app/baum', plan: { link: '/workspace/sample/.local/bin/baum', on_path: true } }, hooks: { agents: ['codex', 'claude-code'].map(agent => ({ agent, name: agent === 'codex' ? 'Codex' : 'Claude Code', detected: true, installed: false, enabled: false, plugin: 'baum@baum' })) } };
  await page.exposeFunction('nativeInvoke', async (command, args) => {
    calls.push([command, args]);
    if (command === 'setup_state') return state;
    if (command === 'github_connection') {
      if (args.action === 'start') github = { ...disconnected, state: 'pending', pending: { id: 'attempt', user_code: 'ABCD-EFGH', verification_uri: 'https://github.com/login/device', expires_at: '2030-01-01T12:00:00Z', interval: 1 } };
      return github;
    }
    if (command === 'repositories') {
      const repo = args.repository;
      switch(args.action) {
        case 'list': return {repositories: ['acme/found','acme/clone','acme/folder'].map(repository=>({repository,description:'',picked:picked.has(repository),clone:localClones.get(repository)||''}))};
        case 'picked': return [...picked].map(repository=>({repository,settings:{},clones:[]}));
        case 'found': return {running:false,scanned_at:'2026-10-23',repositories:{},attached:[]};
        case 'pick': picked.add(repo); return {};
        case 'unpick': picked.delete(repo); return null;
        case 'target': return {dir:'/workspace/sample/code/acme/clone'};
        case 'clone': cloning=true; picked.add(repo); return {state:'running',dir:args.dir};
        case 'status': if(cloning&&repo==='acme/clone'){localClones.set(repo,'/workspace/sample/code/acme/clone');return {state:'done',dir:localClones.get(repo)};} throw Error('No clone started');
        case 'attach': picked.add(repo||'acme/fallback');localClones.set(repo||'acme/fallback',args.path);return {};
        case 'refresh': return {started:true,running:false};
      }
    }
    if (command === 'review_settings') return reviews;
    if (command === 'save_review_settings') { reviews = args.settings; return { ok: true, lines: [] }; }
    if (command === 'choose_repository') { await new Promise(resolve => setTimeout(resolve, 50)); return '/workspace/sample/project'; }
    if (command === 'install_command') { await new Promise(resolve => setTimeout(resolve, 300)); state.command.installed = true; return { ok: true, lines: ['Installed CLI'] }; }
    if (command === 'install_hooks') {
      if (fail) return { ok: false, lines: ['Plugin download failed. Retry.'] };
      Object.assign(state.hooks.agents.find(a => a.agent === args.agent), { installed: true, enabled: true });
    }
    return { ok: true, lines: ['Done'] };
  });
  await page.addInitScript(() => { window.__TAURI_INTERNALS__ = { invoke: (...args) => window.nativeInvoke(...args) }; });
  await page.route('http://setup.test/**', async route => {
    const file = new URL(route.request().url()).pathname.slice(1) || 'setup.html';
    const contentType = file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : file.endsWith('.svg') ? 'image/svg+xml' : file.endsWith('.woff2') ? 'font/woff2' : 'text/html';
    await route.fulfill({ body: await fs.readFile(`${desktop}/dist/${file}`), contentType, headers: contentType === "text/html" ? { "Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src ipc: http://ipc.localhost" } : {} });
  });
  await page.context().route('https://github.com/**', route => route.fulfill({ contentType: 'text/html', body: 'GitHub' }));
  await page.goto('http://setup.test/');
  await page.waitForSelector('#panels');
  assert.equal(await page.locator('#step-label').textContent(), 'STEP 1 OF 3');
  await page.waitForSelector('#github-sign-in');
  assert.equal(await page.locator('#continue').textContent(), 'Skip →');
  await page.screenshot({ path: `${screenshots}/onboarding-github.png` });
  assert(await page.locator('#repo-panel').isHidden(), 'Repositories wait for GitHub');
  await page.locator('#github-sign-in').click();
  await expect(page.getByLabel('GitHub sign-in code')).toHaveText('ABCD-EFGH');
  await page.screenshot({ path: `${screenshots}/onboarding-github-code.png` });
  github = { ...disconnected, state: 'connected', saved: true, login: 'octocat', source: 'baum credential store' };
  await page.waitForSelector('#github-state.ok');
  assert((await page.locator('#github-state').textContent()).includes('octocat'));
  assert.deepEqual(calls.filter(([c, a]) => c === 'github_connection' && a.action).map(([, a]) => a.action), ['start']);
  assert.equal(await page.locator('#continue').textContent(), 'Continue →');
  // Repositories share the first step; a found clone is shown, not picked.
  const foundBox = page.getByRole('checkbox',{name:'Pick acme/found',exact:true});
  await expect(foundBox).toBeVisible();
  await expect(foundBox).not.toBeChecked();
  assert(!calls.some(([c,a])=>c==='repositories'&&a.action==='pick'), 'Nothing is picked without the person');
  assert(await page.locator('#refresh').isVisible(), 'Without a picked repository the step offers to recheck');
  await page.screenshot({ path: `${screenshots}/onboarding-repositories-empty.png` });
  assert(await page.locator('.repository-choice input').first().evaluate(element=>element.getBoundingClientRect().width<24),'Repository checkbox must stay compact under setup input styles');
  await foundBox.check();
  await page.getByRole('checkbox',{name:'Pick acme/clone',exact:true}).check();
  const cloneRow=page.locator('.repository-choices > li').filter({hasText:'acme/clone'});
  await cloneRow.getByRole('button',{name:'Clone it',exact:true}).click();
  await expect(cloneRow.getByLabel('Clone into',{exact:true})).toHaveValue('~/code/acme/clone');
  await cloneRow.getByRole('button',{name:'Clone repository',exact:true}).click();
  await expect(cloneRow).toContainText('~/code/acme/clone');
  await page.getByRole('checkbox',{name:'Pick acme/folder',exact:true}).check();
  await page.locator('.repository-choices > li').filter({hasText:'acme/folder'}).getByRole('button',{name:'Choose folder',exact:true}).click();
  await expect.poll(()=>localClones.get('acme/folder')).toBe('/workspace/sample/project');
  assert(picked.has('acme/found'));
  assert.equal(await page.getByRole('button',{name:'Skip',exact:true}).count(), 0, 'Unpicking is the checkbox alone');
  assert(calls.some(([c,a])=>c==='repositories'&&a.action==='clone'&&a.dir==='/workspace/sample/code/acme/clone'));
  assert(calls.some(([c,a])=>c==='repositories'&&a.action==='attach'&&a.repository==='acme/folder'));
  await expect(page.locator('#refresh')).toBeHidden();
  await page.evaluate(()=>window.scrollTo(0,0));
  await page.screenshot({ path: `${screenshots}/onboarding-repositories.png`, fullPage: true });
  for (const width of [720, 1000]) {
    await page.setViewportSize({ width, height: 800 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Overflow at ${width}`);
  }
  await page.locator('#continue').click();
  assert.equal(await page.locator('#step-label').textContent(), 'STEP 2 OF 3');
  await page.waitForSelector('#review-panel');
  assert.equal(await page.locator('.harness-option input[value=auto]').count(), 0);
  assert(await page.locator('.harness-option input[value=claude]').isChecked(), 'Auto shows as the harness it picks');
  // Auto and an unset model show as explicit choices, which opening baum saves.
  assert.equal(await page.locator('#review-model').inputValue(), 'claude-haiku-5-5');
  assert.equal(await page.locator('#review-model option', { hasText: 'default' }).count(), 0);
  assert.equal(await page.locator('#continue').textContent(), 'Continue →');
  await page.screenshot({ path: `${screenshots}/onboarding-reviews.png` });
  await page.locator('.harness-option:has(input[value=cursor-agent])').click();
  assert.equal(await page.locator('#review-model').inputValue(), 'composer-2.5');
  await page.locator('#review-model').selectOption('Custom…');
  assert(await page.locator('#continue').isDisabled(), 'A custom model needs a name');
  await page.locator('#review-model-custom').fill('composer-3');
  assert(await page.locator('#continue').isEnabled());
  await page.locator('.harness-option:has(input[value=codex])').click();
  assert.equal(await page.locator('#review-model').inputValue(), 'gpt-6-luna');
  await page.locator('#review-model').selectOption('gpt-6-sol');
  await page.locator('#review-automatic').uncheck();
  assert.equal(await page.locator('input[name=pull-requests]').count(), 0, 'Scope shows while automatic reviews are off');
  await page.locator('#review-automatic').check();
  assert(await page.locator('#review-all-note').isHidden(), 'The all note shows for another scope');
  await page.locator('.segmented label', { hasText: 'All' }).click();
  assert(await page.locator('#review-all-note').isVisible(), 'Choosing all explains its reach');
  for (const width of [720, 1000]) {
    await page.setViewportSize({ width, height: 800 });
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Overflow at ${width} with four scopes`);
  }
  await page.screenshot({ path: `${screenshots}/onboarding-reviews-all.png` });
  await page.locator('.segmented label', { hasText: 'Only requested' }).click();
  await page.screenshot({ path: `${screenshots}/onboarding-reviews-changed.png` });
  await page.locator('#continue').click();
  assert.equal(await page.locator('#step-label').textContent(), 'STEP 3 OF 3');
  assert((await page.locator('#command-state').textContent()).includes('~/.local/bin/baum'));
  await page.screenshot({ path: `${screenshots}/onboarding-agents.png` });
  assert.equal(await page.locator('#codex-install').textContent(), 'Connect');
  assert.equal(await page.locator('#claude-code-install').getAttribute('aria-label'), 'Connect Claude Code');
  assert(await page.locator('#refresh').isVisible(), 'An unfinished step should offer to recheck');
  assert.equal(await page.locator('#continue').textContent(), 'Open baum →');
  assert(await page.locator('.agent-logo').evaluateAll(images => images.every(image => image.complete && image.naturalWidth > 0)), 'Bundled icons must load under the native CSP');
  await page.locator('#command-install').click();
  assert.deepEqual(await page.evaluate(() => [...document.querySelectorAll('button.loading')].map(b => b.id)), ['command-install']);
  assert(await page.locator('#log').isHidden(), 'Working should not show the log');
  await page.screenshot({ path: `${screenshots}/onboarding-cli-loading.png` });
  await page.waitForSelector('#command-state.ok');
  assert(await page.locator('#log').isHidden(), 'A successful install should not show the log');
  fail = true;
  await page.locator('#codex-install').click();
  await page.waitForFunction(() => document.querySelector('#log')?.textContent.includes('failed'));
  assert.equal(await page.locator('#step-label').textContent(), 'STEP 3 OF 3');
  fail = false;
  await page.locator('#codex-install').click();
  await page.waitForSelector('#codex-state.ok');
  await page.locator('#claude-code-install').click();
  await page.waitForSelector('#claude-code-state.ok');
  assert(await page.locator('#refresh').isHidden() && await page.locator('#skip-note').isHidden(), 'A finished step should not offer to recheck or skip');
  await page.screenshot({ path: `${screenshots}/onboarding-agents-connected.png` });
  assert.deepEqual(calls.filter(([c]) => c === 'install_hooks').map(([,a]) => a.agent), ['codex', 'codex', 'claude-code']);
  await page.locator('[data-step] button').first().click();
  assert.equal(await page.locator('#step-label').textContent(), 'STEP 1 OF 3');
  await page.locator('[data-step] button').nth(2).click();
  assert.equal(await page.locator('#step-label').textContent(), 'STEP 3 OF 3');
  await page.locator('#back').click();
  assert.equal(await page.locator('#step-label').textContent(), 'STEP 2 OF 3');
  await page.locator('#continue').click();
  assert.equal(await page.locator('#continue').textContent(), 'Open baum →');
  await page.locator('#continue').click();
  await expect.poll(() => calls.some(([c]) => c === 'finish_setup')).toBe(true);
  const saved = calls.filter(([c]) => c === 'save_review_settings');
  assert.deepEqual(saved.map(([, a]) => a.settings), [{ backend: 'codex', model: 'gpt-6-sol', automatic: true, pull_requests: 'requested' }]);
  assert(calls.findIndex(([c]) => c === 'save_review_settings') < calls.findIndex(([c]) => c === 'finish_setup'), 'Settings save before setup finishes');
  await page.reload();
  await page.waitForSelector('#panels');
  for (let step = 0; step < 2; step++) await page.locator('#continue').click();
  assert.equal(await page.locator('#continue').textContent(), 'Open baum →');
  await page.locator('#continue').click();
  await expect.poll(() => calls.filter(([c]) => c === 'finish_setup').length).toBe(2);
  assert.equal(calls.filter(([c]) => c === 'save_review_settings').length, 1, 'Unchanged settings were saved again');
  github=disconnected;
  await page.reload();
  await page.waitForSelector('#github-sign-in');
  assert(await page.locator('#repo-panel').isHidden(), 'Repositories stay hidden while GitHub is disconnected');
  assert.deepEqual(errors, []);
  await browser.close();
  console.log('Onboarding: GitHub sign-in with repositories, review settings, CLI and separate agent installs, retry, back/skip, finish and minimum window width passed.');
})().catch(error => { console.error(error); process.exit(1); });
