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
  // A saved decision model the daemon reports capture ignores with the harness auto resolves to.
  let reviews = { backend: 'auto', model: '', effort: '', automatic: true, pull_requests: 'both', decisions_model: 'gpt-6-luna', decisions_effort: 'high', decisions_backend: 'claude', decisions_effective_model: 'claude-haiku-5-5', decisions_model_ignored: true };
  const state = { first_run: true, home: '/workspace/sample', command: { installed: false, target: '/app/baum', plan: { link: '/workspace/sample/.local/bin/baum', on_path: true } }, hooks: { agents: ['codex', 'claude-code'].map(agent => ({ agent, name: agent === 'codex' ? 'Codex' : 'Claude Code', detected: true, installed: false, enabled: false, plugin: 'baum@baum' })) } };
  await page.exposeFunction('nativeInvoke', async (command, args) => {
    calls.push([command, args]);
    if (command === 'setup_state') return state;
    if (command === 'connection') return { phase: 'connecting' };
    if (command === 'tray_panel') return null;
    if (command === 'github_connection') {
      if (args.action === 'start') github = { ...disconnected, state: 'pending', pending: { id: 'attempt', user_code: 'ABCD-EFGH', verification_uri: 'https://github.com/login/device', expires_at: '2030-01-01T12:00:00Z', interval: 1 } };
      return github;
    }
    if (command === 'repositories') {
      const repo = args.repository;
      switch(args.action) {
        case 'list':
        case 'retry-list': return {searching:Boolean(args.query), repositories: (args.query ? ['acme/found','acme/clone','acme/folder','new-org/never-contributed'].filter(repository=>args.query.toLowerCase().split(/\s+/).every(term=>repository.includes(term))) : ['acme/found','acme/clone','acme/folder']).map(repository=>({repository,description:'',picked:picked.has(repository),clone:localClones.get(repository)||''}))};
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
      Object.assign(state.hooks.agents.find(a => a.agent === args.agent), { installed: true, enabled: true, ...(args.agent === "codex" ? {trust:{state:"untrusted"}} : {}) });
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
  await expect(page.locator('.wordmark svg')).toBeVisible();
  assert.equal(await page.locator('.wordmark img').count(), 0, 'Setup uses Stumpy as its mark');
  assert.equal(await page.locator('#continue').textContent(), 'Continue →');
  await expect(page.locator('#continue')).toBeDisabled();
  await expect(page.locator('#skip-note')).toHaveCount(0);
  await expect(page.locator('[data-step] button').nth(1)).toBeDisabled();
  await expect(page.locator('[data-step] button').nth(2)).toBeDisabled();
  await page.screenshot({ path: `${screenshots}/onboarding-github.png` });
  // Custom colors override default dark recipes, while artwork stays fixed.
  const originalBackground = await page.locator('body').evaluate(element => getComputedStyle(element).backgroundColor);
  const stumpyColor = await page.locator('.wordmark stop').first().getAttribute('stop-color');
  await page.route('http://setup.test/theme-verification.css', route => route.fulfill({ contentType: 'text/css', body: `@layer baum-theme {
    :root[data-color-theme="verification"] {
      --brand-green-600: #345678;
      --surface-canvas: #203040;
      --nav-selected: #304050;
    }
    :root[data-color-theme="verification"][data-theme="dark"] {
      --surface-page: #102030;
      --surface-card: var(--surface-page);
    }
  }` }));
  const customTheme = await page.addStyleTag({ url: 'http://setup.test/theme-verification.css' });
  await page.evaluate(() => { document.documentElement.dataset.colorTheme = 'verification'; });
  await expect.poll(() => page.locator('#github-sign-in').evaluate(element => getComputedStyle(element).backgroundColor)).toBe('rgb(52, 86, 120)');
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  assert.equal(await page.locator('body').evaluate(element => getComputedStyle(element).backgroundColor), 'rgb(16, 32, 48)');
  assert.equal(await page.locator('.setup-rail').evaluate(element => getComputedStyle(element).backgroundColor), 'rgb(32, 48, 64)');
  assert.equal(await page.locator('.setup-card').first().evaluate(element => getComputedStyle(element).backgroundColor), 'rgb(16, 32, 48)');
  assert.equal(await page.locator('[aria-current="step"]').evaluate(element => getComputedStyle(element).backgroundColor), 'rgb(48, 64, 80)');
  assert.equal(await page.locator('.wordmark stop').first().getAttribute('stop-color'), stumpyColor);
  await expect(page.locator('html')).toHaveAttribute('data-color-theme', 'verification');
  await customTheme.evaluate(element => element.remove());
  await page.evaluate(() => { delete document.documentElement.dataset.colorTheme; });
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  assert.equal(await page.locator('body').evaluate(element => getComputedStyle(element).backgroundColor), originalBackground);
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  assert.equal(await page.locator('html').evaluate(element => getComputedStyle(element).colorScheme), 'dark');
  assert.notEqual(await page.locator('body').evaluate(element => getComputedStyle(element).backgroundColor), 'rgb(255, 255, 255)');
  await page.screenshot({ path: `${screenshots}/onboarding-github-dark.png` });
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  assert(await page.locator('#repo-panel').isHidden(), 'Repositories wait for GitHub');
  await page.locator('#github-sign-in').click();
  await expect(page.getByLabel('GitHub sign-in code')).toHaveText('ABCD-EFGH');
  await page.screenshot({ path: `${screenshots}/onboarding-github-code.png` });
  github = { ...disconnected, state: 'connected', saved: true, login: 'octocat', source: 'baum credential store' };
  await page.waitForSelector('#github-state.ok');
  assert((await page.locator('#github-state').textContent()).includes('octocat'));
  assert.deepEqual(calls.filter(([c, a]) => c === 'github_connection' && a.action).map(([, a]) => a.action), ['start']);
  assert.equal(await page.locator('#continue').textContent(), 'Continue →');
  await expect(page.locator('#continue')).toBeDisabled();
  // Repositories share the first step; a found clone is shown, not picked.
  const foundBox = page.getByRole('checkbox',{name:'Pick acme/found',exact:true});
  await expect(foundBox).toBeVisible();
  await expect(foundBox).not.toBeChecked();
  assert(!calls.some(([c,a])=>c==='repositories'&&a.action==='pick'), 'Nothing is picked without the person');
  assert(await page.locator('#refresh').isVisible(), 'Without a picked repository the step offers to recheck');
  await page.screenshot({ path: `${screenshots}/onboarding-repositories-empty.png` });
  const search = page.getByRole('searchbox');
  await search.fill('new-org never');
  const searchedBox = page.getByRole('checkbox',{name:'Pick new-org/never-contributed',exact:true});
  await expect(searchedBox).toBeVisible();
  await expect(page.getByText('Searching your GitHub repositories…',{exact:true})).toBeVisible();
  await searchedBox.check();
  await expect(page.locator('#continue')).toBeEnabled();
  await expect(page.getByText('Searching your GitHub repositories…',{exact:true})).toBeVisible();
  await search.fill('');
  await expect(searchedBox).toBeChecked();
  await searchedBox.uncheck();
  await expect(page.locator('#continue')).toBeDisabled();
  await expect(foundBox).toBeVisible();
  assert(await page.locator('.repository-choice input').first().evaluate(element=>element.getBoundingClientRect().width<24),'Repository checkbox must stay compact under setup input styles');
  await foundBox.check();
  await expect(page.locator('#continue')).toBeEnabled();
  await expect(page.locator('[data-step] button').nth(1)).toBeEnabled();
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
  assert.deepEqual(await page.locator('.harness-option input').evaluateAll(inputs => inputs.map(input => input.value)), ['claude', 'codex']);
  assert.equal(await page.locator('.harness-option input[value=auto]').count(), 0);
  assert(await page.locator('.harness-option input[value=claude]').isChecked(), 'Auto shows as the harness it picks');
  // Auto and an unset model show as explicit choices, which opening baum saves.
  assert.equal(await page.locator('#review-model').inputValue(), 'claude-haiku-5-5');
  assert.deepEqual(await page.locator('#review-effort option').evaluateAll(options => options.map(option => option.value)), ['', 'low', 'medium', 'high', 'xhigh', 'max']);
  await page.locator('#review-effort').selectOption('max');
  assert.equal(await page.locator('#review-model option', { hasText: 'default' }).count(), 0);
  assert.equal(await page.locator('#decisions-model, #decisions-effort').count(), 0, 'Extraction preferences are configured in Settings');
  await expect(page.locator('#review-panel')).not.toContainText('baum captures the decisions');
  assert.equal(await page.locator('#continue').textContent(), 'Continue →');
  await page.screenshot({ path: `${screenshots}/onboarding-reviews.png`, fullPage: true });
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  assert.notEqual(
    await page.locator('.harness-tile').first().evaluate(element => getComputedStyle(element).backgroundColor),
    await page.locator('.harness-tile').nth(1).evaluate(element => getComputedStyle(element).backgroundColor),
    'The selected harness tile must be distinct in dark mode',
  );
  await page.screenshot({ path: `${screenshots}/onboarding-reviews-dark.png`, fullPage: true });
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  await page.locator('#review-model').selectOption('Custom…');
  assert(await page.locator('#continue').isDisabled(), 'A custom model needs a name');
  assert(await page.locator('#review-effort').isDisabled(), 'An unknown custom model leaves effort to the agent');
  await page.locator('#review-model-custom').fill('custom-claude-model');
  assert(await page.locator('#continue').isEnabled());
  await page.locator('.harness-option:has(input[value=codex])').click();
  assert.equal(await page.locator('#review-model').inputValue(), 'gpt-6-luna');
  assert.equal(await page.locator('#review-effort option[value=ultra]').count(), 0, 'Luna does not advertise Ultra');
  await page.locator('#review-model').selectOption('gpt-6.1-sol');
  await page.locator('#review-effort').selectOption('ultra');
  await page.locator('#review-model').selectOption('gpt-6-luna');
  assert.equal(await page.locator('#review-effort').inputValue(), '', 'Switching to Luna resets unsupported Ultra effort');
  await page.locator('#review-model').selectOption('gpt-6.1-sol');
  await page.locator('#review-effort').selectOption('ultra');
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
  await expect(page.locator('#continue')).toBeEnabled();
  await expect(page.locator('#skip-note')).toHaveText('You can finish setup later from the settings.');
  await page.screenshot({ path: `${screenshots}/onboarding-agents.png` });
  await page.emulateMedia({ colorScheme: 'dark' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
  await page.screenshot({ path: `${screenshots}/onboarding-agents-dark.png` });
  await page.emulateMedia({ colorScheme: 'light' });
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  assert.equal(await page.locator('#codex-install').textContent(), 'Connect');
  assert.equal(await page.locator('#claude-code-install').getAttribute('aria-label'), 'Connect Claude Code');
  assert(await page.locator('#refresh').isVisible(), 'An unfinished step should offer to recheck');
  assert.equal(await page.locator('#continue').textContent(), 'Open baum →');
  assert(await page.locator('.agent-logo').evaluateAll(images => images.every(image => image.tagName.toLowerCase() === 'svg')), 'Vendor icons use palette-aware SVG marks');
  await page.locator('#command-install').click();
  assert.deepEqual(await page.evaluate(() => [...document.querySelectorAll('button.loading')].map(b => b.id)), ['command-install']);
  assert(await page.locator('#log').isHidden(), 'Working should not show the log');
  await page.screenshot({ path: `${screenshots}/onboarding-cli-loading.png` });
  await page.waitForSelector('#command-state.ok');
  await expect(page.locator('#continue')).toBeEnabled();
  await expect(page.locator('#skip-note')).toHaveText('You can finish setup later from the settings.');
  assert(await page.locator('#log').isHidden(), 'A successful install should not show the log');
  fail = true;
  await page.locator('#codex-install').click();
  await page.waitForFunction(() => document.querySelector('#log')?.textContent.includes('failed'));
  assert.equal(await page.locator('#step-label').textContent(), 'STEP 3 OF 3');
  fail = false;
  await page.locator('#codex-install').click();
  await expect(page.locator('#codex-state')).toHaveText('Needs approval in Codex');
  assert(!await page.locator('#codex-state').evaluate(e=>e.classList.contains('ok')), 'Installed hooks must not imply trusted');
  await expect(page.locator('#codex-hint')).toContainText('/hooks');
  await page.locator('#claude-code-install').click();
  await page.waitForSelector('#claude-code-state.ok');
  await expect(page.locator('#refresh')).toBeVisible();
  state.hooks.agents.find(a=>a.agent==='codex').trust.state='trusted';
  await page.locator('#refresh').click();
  await expect(page.locator('#codex-state.ok')).toHaveText('Connected');
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
  assert.deepEqual(saved.map(([, a]) => a.settings), [{ backend: 'codex', model: 'gpt-6.1-sol', effort: 'ultra', automatic: true, pull_requests: 'requested' }]);
  assert(calls.findIndex(([c]) => c === 'save_review_settings') < calls.findIndex(([c]) => c === 'finish_setup'), 'Settings save before setup finishes');
  await page.reload();
  await page.waitForSelector('#panels');
  for (let step = 0; step < 2; step++) await page.locator('#continue').click();
  assert.equal(await page.locator('#continue').textContent(), 'Open baum →');
  await page.locator('#continue').click();
  await expect.poll(() => calls.filter(([c]) => c === 'finish_setup').length).toBe(2);
  assert.equal(calls.filter(([c]) => c === 'save_review_settings').length, 1, 'Unchanged settings were saved again');
  // Opening the app uses its own CLI, without installing a terminal command or hooks.
  state.command.installed = false;
  for (const agent of state.hooks.agents) Object.assign(agent, { installed: false, enabled: false });
  await page.reload();
  await page.waitForSelector('#panels');
  for (let step = 0; step < 2; step++) await page.locator('#continue').click();
  await expect(page.locator('#command-install')).toBeVisible();
  await expect(page.locator('#continue')).toBeEnabled();
  await page.locator('#continue').click();
  await expect.poll(() => calls.filter(([c]) => c === 'finish_setup').length).toBe(3);
  assert.equal(calls.filter(([c]) => c === 'install_command').length, 1, 'Finishing must not install a terminal command');
  assert.equal(calls.filter(([c]) => c === 'install_hooks').length, 3, 'Finishing must not install agent hooks');
  for (const agent of state.hooks.agents) agent.detected = false;
  await page.reload();
  await page.waitForSelector('#panels');
  await page.locator('#continue').click();
  await expect(page.locator('#agent-installation')).toContainText('Install an agent to continue');
  await expect(page.locator('#review-model')).toHaveCount(0);
  await expect(page.locator('.harness-option input:checked')).toHaveCount(0);
  await expect(page.locator('.harness-option input:disabled')).toHaveCount(2);
  await expect(page.locator('#continue')).toBeDisabled();
  await expect(page.locator('[data-step] button').nth(2)).toBeDisabled();
  state.hooks.agents.find(agent => agent.agent === 'codex').detected = true;
  await page.locator('#refresh').click();
  await expect(page.locator('.harness-option input[value=codex]')).toBeChecked();
  await expect(page.locator('.harness-option input[value=claude]')).toBeDisabled();
  await expect(page.locator('#review-model')).toHaveValue('gpt-6-luna');
  await expect(page.locator('#continue')).toBeEnabled();
  github=disconnected;
  await page.reload();
  await page.waitForSelector('#github-sign-in');
  assert(await page.locator('#repo-panel').isHidden(), 'Repositories stay hidden while GitHub is disconnected');
  await expect(page.locator('#continue')).toBeDisabled();
  await expect(page.locator('[data-step] button').nth(2)).toBeDisabled();
  for (const screen of ['index.html', 'tray.html']) {
    await page.goto(`http://setup.test/${screen}`);
    await page.emulateMedia({ colorScheme: 'dark' });
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    assert.notEqual(await page.locator('body').evaluate(element => getComputedStyle(element).backgroundColor), 'rgb(255, 255, 255)');
    await page.emulateMedia({ colorScheme: 'light' });
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
  }
  assert.deepEqual(errors, []);
  await browser.close();
  console.log('Onboarding: required GitHub sign-in and repository selection, review settings, CLI and separate agent installs, retry, back navigation, finish and minimum window width passed.');
})().catch(error => { console.error(error); process.exit(1); });
