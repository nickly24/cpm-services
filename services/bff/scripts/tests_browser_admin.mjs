import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';

const root = new URL('../../../', import.meta.url).pathname;
const { password } = JSON.parse(await fs.readFile(root + 'dev/local/accounts.json', 'utf8'));
const demo = JSON.parse(await fs.readFile(root + 'dev/local/bff-tests-demo.json', 'utf8'));
const prefix = 'DEV UI Tests ' + Date.now();
const rolesOnly = process.argv.includes('--roles-only');
const canvasOnly = process.argv.includes('--canvas-only');
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const checks = [], errors = [], external = [], alerts = [], fixtures = {};
let page, context, revision;
const ok = (name) => { checks.push(name); console.log(JSON.stringify({ check: name })); };
function responseFor(predicate) {
  const pending = page.waitForResponse(predicate);
  // Attach immediately so a slow click cannot cause an unhandled timeout before
  // the surrounding try/finally gets to await the response.
  void pending.catch(() => {});
  return pending;
}
function card(title) { return page.locator('article').filter({ has: page.getByRole('heading', { name: title, exact: true }) }); }
async function login(name) {
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await context.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.hostname !== '127.0.0.1') { external.push(url.origin + url.pathname); return route.abort(); }
    return route.continue();
  });
  page = await context.newPage();
  page.setDefaultTimeout(15000);
  page.setDefaultNavigationTimeout(30000);
  page.on('pageerror', error => errors.push(error.message));
  page.on('dialog', async dialog => {
    if (dialog.type() === 'alert') alerts.push(dialog.message());
    await dialog.accept();
  });
  await page.goto('http://127.0.0.1:3001/login');
  await page.getByLabel('Логин', { exact: true }).fill(name);
  await page.getByLabel('Пароль', { exact: true }).fill(password);
  const logged = responseFor(r => r.url().endsWith('/bff/auth/login') && r.request().method() === 'POST');
  await page.getByRole('button', { name: 'Войти', exact: true }).click();
  revision = (await (await logged).json()).context_revision;
  await page.waitForURL('**/cabinet/**');
  await page.getByLabel('Переключить кабинет и подразделение').waitFor();
}
async function catalog() {
  await page.goto('http://127.0.0.1:3001/cabinet/admin/tests');
  await page.getByRole('button', { name: 'Тесты подразделения', exact: true }).waitFor();
  await page.getByPlaceholder('Название теста…').waitFor();
  await page.getByText('Загрузка тестов…', { exact: true }).waitFor({ state: 'hidden' });
}
async function switchDivision(value) {
  await page.waitForTimeout(250);
  const deadline = Date.now() + 15000;
  let idle = false;
  while (Date.now() < deadline) {
    const state = await page.evaluate(async () => navigator.locks.query());
    idle = ![...state.held, ...state.pending].some(lock => lock.name === 'cpm-bff-context');
    if (idle) break;
    await page.waitForTimeout(100);
  }
  assert.equal(idle, true, 'editor/action context guard is released before switching');
  const switched = responseFor(r => r.url().endsWith('/bff/auth/context') && r.request().method() === 'POST');
  await page.getByLabel('Переключить кабинет и подразделение').selectOption(value);
  assert.equal((await switched).ok(), true);
  await page.getByLabel('Переключить кабинет и подразделение').waitFor();
  await page.waitForFunction(v => document.querySelector('select[aria-label="Переключить кабинет и подразделение"]')?.value === v, value);
  // Context switch performs a full navigation; DevTools may discard its response body.
  // Read the session only after the new cabinet has rendered.
  const identity = await context.request.get('http://127.0.0.1:3001/bff/auth/me', { headers: { 'X-CPM-Request': '1' } });
  assert.equal(identity.ok(), true);
  revision = (await identity.json()).context_revision;
  await catalog();
}
async function action(title, name, route) {
  const changed = responseFor(r => new URL(r.url()).pathname.endsWith(route) && r.request().method() !== 'GET');
  await card(title).getByRole('button', { name, exact: true }).click();
  const response = await changed;
  assert.equal(response.ok(), true, `${name} ${response.status()}`);
  const body = await response.json();
  await page.waitForFunction(() => !document.querySelector('article button:disabled'));
  await page.getByText('Загрузка тестов…', { exact: true }).waitFor({ state: 'hidden' });
  await page.waitForTimeout(150);
  return body.data;
}
async function snapshot(id) {
  const response = await context.request.get('http://127.0.0.1:3001/bff/tests/' + id + '/content', {
    headers: { 'X-CPM-Request': '1', 'X-CPM-Context': revision }, timeout: 30000,
  });
  assert.equal(response.ok(), true);
  return (await response.json()).data;
}
async function noOverflow(label) {
  await page.waitForTimeout(250);
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const dimensions = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth,
    offenders: [...document.querySelectorAll('body *')].map(element => ({tag:element.tagName, class:element.className, bounds:element.getBoundingClientRect().toJSON()})).filter(element => element.bounds.width && element.bounds.right > innerWidth + 1).slice(0,10) }));
  assert.ok(dimensions.document <= dimensions.viewport + 1, `${label} overflow ${JSON.stringify(dimensions)}`);
  ok(label);
}
async function canvasDraft() {
  await catalog();
  await page.getByRole('button', { name: /^Драфты/ }).click();
  const created = responseFor(r => r.url().endsWith('/bff/tests/drafts') && r.request().method() === 'POST');
  await page.getByRole('button', { name: 'Новый драфт', exact: true }).click();
  const draft = (await (await created).json()).data;
  fixtures.canvas_draft_id = draft.id;
  await page.getByRole('button', { name: 'Сохранить как тест', exact: true }).waitFor();
  const canvasBounds = await page.locator('.react-flow').boundingBox();
  assert.ok(canvasBounds?.height > 400, 'canvas has a visible usable height after lock acknowledgement');
  await page.getByRole('button', { name: 'Открыть настройки', exact: true }).click();
  const title = prefix + ' · Canvas';
  await page.getByLabel('Название теста', { exact: true }).fill(title);
  await page.getByLabel('Направление').selectOption({ label: 'DEV BFF Tests ' + demo.run + ' Направление' });
  await page.getByRole('button', { name: 'Вопрос', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Текстовый ответ', exact: true }).click();
  const textSpan = page.locator('span[data-inline-editable]').filter({ hasText: /^Текст вопроса$/ });
  if (await textSpan.count()) await textSpan.click();
  await page.locator('textarea[data-inline-editable]').fill('Назовите столицу России');
  await page.getByRole('heading', { name: title, exact: true }).click();
  const answerChip = page.locator('[data-answer-chip]').first();
  await answerChip.getByText('Текстовый ответ', { exact: true }).click();
  // Answer chips select on the first click, then edit on the second.
  await answerChip.getByText('Текстовый ответ', { exact: true }).click();
  await page.locator('textarea[data-inline-editable]').fill('Москва');
  await page.getByRole('heading', { name: title, exact: true }).click();
  const savedDeadline = Date.now() + 15000;
  let saved;
  while (Date.now() < savedDeadline) {
    const response = await context.request.get('http://127.0.0.1:3001/bff/tests/drafts/' + draft.id, { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': revision } });
    saved = (await response.json()).data;
    if (saved?.title === title && saved.questions?.[0]?.correct_answers?.[0] === 'Москва') break;
    await page.waitForTimeout(250);
  }
  assert.equal(saved?.questions?.[0]?.correct_answers?.[0], 'Москва', 'canvas autosave stores text key under the real draft lock');
  assert.equal(saved.lock?.actor_id > 0, true);
  ok('canvas draft creates a locked editor and autosaves title/direction/text question/key');
  await page.screenshot({ path: root + 'dev/local/bff-tests-admin-canvas.png', fullPage: true });
  const collapse = page.getByRole('button', { name: 'Закрыть настройки', exact: true });
  if (await collapse.count()) await collapse.click();
  await page.waitForTimeout(250);
  const published = responseFor(r => r.url().endsWith('/bff/tests/drafts/' + draft.id + '/publish') && r.request().method() === 'POST');
  await page.getByRole('button', { name: 'Сохранить как тест', exact: true }).click();
  const test = (await (await published).json()).data; fixtures.canvas_test_id = test.id;
  await catalog();
  await page.getByPlaceholder('Название теста…').waitFor();
  await page.getByPlaceholder('Название теста…').fill(title);
  await card(title).waitFor();
  assert.equal(await card(title).getByLabel('Видимость теста', { exact: true }).isChecked(), false);
  assert.equal((await snapshot(test.id)).questions[0].correct_answers[0], 'Москва');
  ok('canvas UI publishes one independent hidden test with its full content');
}

try {
  if (canvasOnly) { await login('dev_admin'); await canvasDraft(); }
  else if (!rolesOnly) {
  await login('dev_admin'); await catalog();
  await page.getByLabel('Направление').selectOption({ label: 'DEV BFF Tests ' + demo.run + ' Направление' });
  await page.getByPlaceholder('Название теста…').fill('DEV BFF Tests ' + demo.run + ' · Общий тест');
  await card('DEV BFF Tests ' + demo.run + ' · Общий тест').waitFor();
  await page.getByLabel('Связь с подразделением').selectOption('owned');
  await page.getByLabel('Владелец').selectOption('1002');
  await page.getByText('Тестов пока нет', { exact: true }).waitFor();
  await page.getByLabel('Владелец').selectOption('1001');
  await card('DEV BFF Tests ' + demo.run + ' · Общий тест').waitFor();
  ok('catalog direction/search/owner/relation filters intersect before showing rows');
  await page.getByLabel('Связь с подразделением').selectOption('all');
  await page.getByLabel('Владелец').selectOption('');
  await page.getByPlaceholder('Название теста…').fill('');
  await page.getByRole('button', { name: 'Создать тест', exact: true }).click();
  await page.getByRole('menuitem', { name: 'Тест в системе', exact: true }).click();
  await page.getByRole('heading', { name: 'Создание нового теста', exact: true }).waitFor();
  await page.getByLabel('Название теста', { exact: true }).fill(prefix);
  await page.getByLabel('Направление').selectOption({ label: 'DEV BFF Tests ' + demo.run + ' Направление' });
  await page.getByLabel('Описание', { exact: true }).fill('Создано полностью в административном UI');
  await page.getByRole('button', { name: '+ Текстовый', exact: true }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Текст вопроса', { exact: true }).fill('Какая столица России?');
  await dialog.getByLabel('Баллы', { exact: true }).fill('5');
  if (await dialog.locator('input:not([type="number"])').count() === 0) await dialog.getByRole('button', { name: '+ Ответ', exact: true }).click();
  await dialog.locator('input:not([type="number"])').first().fill('Москва');
  await dialog.getByRole('button', { name: 'Сохранить вопрос', exact: true }).click();
  const created = responseFor(r => r.url().endsWith('/bff/tests/catalog') && r.request().method() === 'POST');
  await page.getByRole('button', { name: 'Создать тест', exact: true }).click();
  const source = (await (await created).json()).data; fixtures.source_test_id = source.id;
  await page.getByPlaceholder('Название теста…').fill(prefix);
  await card(prefix).waitFor();
  assert.equal(await card(prefix).getByLabel('Видимость теста', { exact: true }).isChecked(), false);
  const sourceContent = await snapshot(source.id);
  assert.equal(sourceContent.questions[0].correct_answers[0], 'Москва');
  ok('classic UI creates a hidden test with text question, points and answer key');
  await action(prefix, 'Открыть в банк', '/bank');
  await card(prefix).getByRole('button', { name: 'Закрыть банк', exact: true }).waitFor();
  ok('UI opens the source in the organization bank');
  await switchDivision('1001:1002');
  await page.getByRole('button', { name: 'Банк организации', exact: true }).click();
  await page.getByPlaceholder('Название теста…').fill(prefix);
  await card(prefix).getByRole('button', { name: 'Подключить', exact: true }).waitFor();
  await action(prefix, 'Подключить', '/attach');
  await card(prefix).getByRole('button', { name: 'Снять подключение', exact: true }).waitFor();
  assert.equal(await card(prefix).getByLabel('Видимость теста', { exact: true }).isChecked(), false);
  ok('attachment keeps common test identity and starts hidden in reserve division');
  const cloned = await action(prefix, 'Независимая копия', '/copy'); fixtures.copy_test_id = cloned.id;
  await card(prefix + ' — копия').waitFor();
  const copiedContent = await snapshot(cloned.id);
  assert.notEqual(copiedContent.content_version_id, sourceContent.content_version_id);
  assert.deepEqual(copiedContent.questions, sourceContent.questions);
  ok('UI deep copy preserves the question/key/weight in a separate version');
  await action(prefix, 'Снять подключение', '/attachment');
  await card(prefix).getByRole('button', { name: 'Подключить', exact: true }).waitFor();
  ok('UI detaches the common test without deleting its independent copy');
  await page.getByRole('button', { name: 'Тесты подразделения', exact: true }).click();
  await page.getByLabel('Связь с подразделением').selectOption('copy');
  await card(prefix + ' — копия').waitFor();
  assert.equal(await card(prefix).count(), 0);
  await noOverflow('desktop tests filters and cards fit the viewport');
  await page.screenshot({ path: root + 'dev/local/bff-tests-admin-desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await noOverflow('mobile tests filters and cards fit the viewport');
  await page.screenshot({ path: root + 'dev/local/bff-tests-admin-mobile.png', fullPage: true });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await switchDivision('1001:1001');
  await page.getByPlaceholder('Название теста…').fill(prefix);
  await card(prefix).waitFor();
  await action(prefix, 'Архивировать', '/archive');
  await page.getByLabel('Архив').selectOption('true');
  await card(prefix).getByRole('button', { name: 'Восстановить', exact: true }).waitFor();
  await switchDivision('1001:1002');
  await page.getByPlaceholder('Название теста…').fill(prefix);
  await card(prefix + ' — копия').waitFor();
  assert.deepEqual((await snapshot(cloned.id)).questions, copiedContent.questions);
  ok('archiving the UI source preserves its independent copy in another division');
  // Read-only monitoring uses existing API fixtures with confirmed history.
  await switchDivision('1001:1001');
  await page.getByPlaceholder('Название теста…').fill('DEV BFF Tests ' + demo.run + ' · Общий тест');
  await card('DEV BFF Tests ' + demo.run + ' · Общий тест').getByRole('button', { name: 'Открыть', exact: true }).click();
  await page.getByRole('button', { name: 'Сдачи', exact: true }).click();
  await page.getByLabel('Статус проверки', { exact: true }).selectOption('ready');
  const gradedRow = page.locator('tbody tr').filter({ has: page.getByRole('cell', { name: '50', exact: true }) }).first();
  await gradedRow.waitFor(); await gradedRow.getByRole('button', { name: 'Детали', exact: true }).click();
  await page.getByText('История расчётов', { exact: true }).click();
  await page.getByText('50% · 8/16', { exact: true }).waitFor();
  await page.getByText('80% · 8/10', { exact: true }).waitFor();
  ok('admin monitor displays real ready status and both confirmed calculation results');
  await canvasDraft();
  }
  if (!canvasOnly) {
  await login(demo.staff_login); await catalog();
  assert.equal(await page.getByRole('button', { name: 'Создать тест', exact: true }).count(), 0);
  await page.getByPlaceholder('Название теста…').fill('DEV BFF Tests ' + demo.run + ' · Общий тест');
  const viewCard = card('DEV BFF Tests ' + demo.run + ' · Общий тест'); await viewCard.waitFor();
  for (const name of ['Независимая копия', 'Архивировать', 'Редактировать', 'Закрыть банк']) assert.equal(await viewCard.getByRole('button', { name, exact: true }).count(), 0);
  assert.equal(await viewCard.getByLabel('Видимость теста', { exact: true }).isDisabled(), true);
  ok('view-only staff has no create/edit/copy/archive mutation controls');
  await switchDivision('1001:1002');
  await page.getByRole('button', { name: 'Создать тест', exact: true }).waitFor();
  await page.getByPlaceholder('Название теста…').fill('DEV BFF Tests ' + demo.run + ' · Общий тест');
  const editCard = card('DEV BFF Tests ' + demo.run + ' · Общий тест'); await editCard.waitFor();
  await editCard.getByRole('button', { name: 'Независимая копия', exact: true }).waitFor();
  assert.equal(await editCard.getByRole('button', { name: 'Архивировать', exact: true }).count(), 0);
  ok('staff division switch grants local settings/copy edit while preserving owner boundaries');
  await login(demo.results_login);
  await page.waitForURL('**/cabinet/staff_admin/test-results');
  await page.getByRole('heading', { name: 'Результаты тестов', exact: true }).waitFor();
  await page.getByLabel('Поиск ученика по имени, ID или классу').fill(String(demo.student_account_id));
  await page.getByRole('button', { name: 'Открыть результаты: Аккаунт ' + demo.student_account_id, exact: true }).click();
  await page.getByLabel('Поиск результатов по названию теста').fill(demo.source_test_id);
  await page.getByRole('button').filter({ has: page.getByRole('heading', { name: 'Тест ' + demo.source_test_id, exact: true }) }).waitFor();
  await page.getByText('50%', { exact: true }).first().waitFor();
  ok('results-only staff lands in allowed results and sees rows without tests/users permissions');
  await page.screenshot({ path: root + 'dev/local/bff-tests-results-only.png', fullPage: true });
  await login('dev_admin_math'); await catalog();
  await page.getByRole('button', { name: 'Банк организации', exact: true }).click();
  await page.getByPlaceholder('Название теста…').fill(prefix);
  await page.getByText(/По фильтрам ничего не найдено|Тестов пока нет/).waitFor();
  assert.equal(await card(prefix).count(), 0);
  ok('another organization administrator cannot see the UI source or copies');
  }
  assert.deepEqual(errors, []); assert.deepEqual(external, []); assert.deepEqual(alerts, []);
  ok('no browser errors, unexpected alerts or external network requests');
  await fs.writeFile(root + 'dev/local/bff-tests-admin-browser-demo.json', JSON.stringify({ prefix, ...fixtures }, null, 2));
  await fs.writeFile(root + 'dev/local/bff-tests-admin-browser-report.json', JSON.stringify({ status: 'passed', checks, fixtures }, null, 2));
  console.log(JSON.stringify({ status: 'passed', checks, fixtures }));
} catch (error) {
  const report = { status: 'failed', checks, fixtures, errors, external, alerts, failure: String(error) };
  console.log(JSON.stringify(report));
  await fs.writeFile(root + 'dev/local/bff-tests-admin-browser-report.json', JSON.stringify(report, null, 2));
  if (page) {
    console.log('locks after failure', JSON.stringify(await page.evaluate(async () => navigator.locks.query())));
    console.log('canvas geometry', JSON.stringify(await page.evaluate(() => [...document.querySelectorAll('[data-inline-editable],.react-flow,.react-flow__viewport,.react-flow__node,[class*="canvasShell"],[class*="__canvas"]')].map(element => ({class:element.className, bounds:element.getBoundingClientRect().toJSON(), transform:getComputedStyle(element).transform, text:(element.innerText||'').slice(0,50)})))));
    console.log((await page.locator('body').innerText()).slice(-5000));
    await page.screenshot({ path: root + 'dev/local/bff-tests-admin-browser-failure.png', fullPage: true });
  }
  throw error;
} finally { await browser.close(); }
