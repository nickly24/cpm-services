/** Existing Access workspace; public owned fixtures, repeatable --read-only after publication. */
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const root = new URL('../../../', import.meta.url).pathname;
const base = 'http://127.0.0.1:3001', readOnly = process.argv.includes('--read-only');
const { password } = JSON.parse(await fs.readFile(root + 'dev/local/accounts.json', 'utf8'));
const manifest = root + 'dev/local/bff-core-access-demo.json';
const checks = [], errors = [], external = [], legacy = [], writes = [];
let demo, context, page, revision, cabinet = 'admin';
const ok = name => { checks.push(name); console.log(JSON.stringify({ check: name })); };
async function owner(token, path, method = 'GET', body) {
  assert(!readOnly, 'Read-only must not access fixture owner APIs');
  const response = await fetch((path.startsWith('/v1/auth/') ? 'http://127.0.0.1:8002' : 'http://127.0.0.1:8003') + path, { method, headers: { ...(token ? { Authorization: `Bearer ${token}` } : {}), ...(body ? { 'Content-Type': 'application/json' } : {}) }, ...(body ? { body: JSON.stringify(body) } : {}) });
  const result = await response.json(); assert(response.ok, `${method} ${path}: ${response.status} ${result.error ?? ''}`); return result.data ?? result;
}
async function token(division) { const auth = await owner(null, '/v1/auth/login', 'POST', { login: 'dev_admin', password, organization_id: 1001, division_id: division }); return auth.access_token ?? auth.token; }
async function prepare() {
  const run = Date.now().toString(36), auth = await token(1001), prefix = `DEV Access ${run}`;
  const a = (await owner(auth, '/v1/core/divisions', 'POST', { name: `${prefix} A` })).id;
  const b = (await owner(auth, '/v1/core/divisions', 'POST', { name: `${prefix} B` })).id;
  const aToken = await token(a), bToken = await token(b);
  const bootstrap = (await owner(aToken, '/v1/core/roles', 'POST', { name: `${prefix} viewer`, permissions: { users: { view: true, edit: false } } })).id;
  const viewerLogin = `access_${run}_viewer`, foreignLogin = `access_${run}_foreign`;
  const viewerId = (await owner(aToken, `/v1/core/divisions/${a}/users`, 'POST', { role: 'staff_admin', full_name: `${prefix} viewer`, login: viewerLogin, password, role_id: bootstrap, class: 0 })).account_id;
  const foreignId = (await owner(bToken, `/v1/core/divisions/${b}/users`, 'POST', { role: 'staff_admin', full_name: `${prefix} foreign`, login: foreignLogin, password, role_id: bootstrap, class: 0 })).account_id;
  demo = { run, prefix, organization_id: 1001, division_a: a, division_b: b, bootstrap_role_id: bootstrap, viewer: { account_id: viewerId, login: viewerLogin }, foreign: { account_id: foreignId, full_name: `${prefix} foreign` } };
  await fs.writeFile(manifest, JSON.stringify(demo, null, 2));
}
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
function responseFor(path, method = 'GET') { const promise = page.waitForResponse(response => new URL(response.url()).pathname === path && response.request().method() === method); void promise.catch(() => {}); return promise; }
async function login(login = 'dev_admin') {
  if (context) await context.close();
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 } });
  await context.route('**/*', route => {
    const url = new URL(route.request().url());
    if (url.hostname !== '127.0.0.1') { external.push(url.origin + url.pathname); return route.abort(); }
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/bff/unconnected')) legacy.push(url.pathname);
    return route.continue();
  });
  page = await context.newPage(); page.setDefaultTimeout(20000); page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => dialog.accept());
  page.on('request', request => { const path = new URL(request.url()).pathname; if (!['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(path)) writes.push({ path, method: request.method() }); });
  await page.goto(base + '/login'); await page.getByLabel('Логин').fill(login); await page.getByLabel('Пароль').fill(password);
  const pending = responseFor('/bff/auth/login', 'POST'); await page.getByRole('button', { name: 'Войти', exact: true }).click(); const result = await (await pending).json(); assert(result.status); revision = result.context_revision; cabinet = result.user.role;
  await page.waitForURL('**/cabinet/**'); await page.waitForLoadState('networkidle');
}
async function switchDivision(division) {
  const select = page.getByLabel('Переключить кабинет и подразделение'); await select.waitFor(); const value = `${demo.organization_id}:${division}`;
  if (await select.inputValue() !== value) { const navigated = page.waitForEvent('load'); void navigated.catch(() => {}); const pending = responseFor('/bff/auth/context', 'POST'); await select.selectOption(value); assert((await pending).ok()); await navigated; await page.waitForLoadState('networkidle'); }
  revision = (await (await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } })).json()).context_revision;
}
async function read(path) { const response = await context.request.get(base + '/bff' + path, { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': revision } }); assert(response.ok(), `${path}: ${response.status()}`); return (await response.json()).data; }
async function staff() { return read('/core/users?role=staff_admin&limit=200&offset=0'); }
async function openAccess() { await page.goto(`${base}/cabinet/${cabinet}/access`); await page.getByRole('heading', { name: 'Доступ и роли', exact: true }).waitFor(); await page.getByText('Загрузка ролей…', { exact: true }).waitFor({ state: 'hidden' }); }
function card(name) { return page.locator('article').filter({ has: page.getByRole('heading', { name, exact: true }) }); }
function row(name) { return page.locator('tbody tr').filter({ has: page.getByText(name, { exact: true }) }); }
const dialog = () => page.getByRole('dialog');
async function usersTab() { await page.getByRole('button', { name: /^Пользователи \d+$/ }).click(); }
async function rolesTab() { await page.getByRole('button', { name: /^Роли \d+$/ }).click(); }
async function createRole(name, loseResponse = false) {
  await rolesTab(); await page.getByRole('button', { name: '+ Создать роль', exact: true }).click(); await page.getByLabel('Название роли').fill(name);
  assert.equal(await page.getByLabel('Описание', { exact: true }).count(), 0);
  await page.getByLabel('Пользователи: редактирование', { exact: true }).check(); assert(await page.getByLabel('Пользователи: просмотр', { exact: true }).isChecked());
  const before = writes.filter(write => write.path === '/bff/core/roles' && write.method === 'POST').length;
  if (loseResponse) {
    let lost = false;
    await page.route('**/bff/core/roles', async route => { if (route.request().method() !== 'POST' || lost) return route.continue(); const response = await route.fetch(); assert(response.ok()); lost = true; await route.abort('failed'); });
  }
  const pending = loseResponse ? null : responseFor('/bff/core/roles', 'POST'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click();
  if (pending) assert((await pending).ok());
  else {
    await page.getByRole('button', { name: 'Проверить результат', exact: true }).waitFor(); assert.equal(await page.evaluate(secret => Object.values(sessionStorage).some(value => value.includes(secret)), password), false);
    await page.reload(); await page.getByRole('button', { name: 'Проверить результат', exact: true }).waitFor(); assert(await page.getByRole('button', { name: '+ Создать роль', exact: true }).isDisabled());
    await page.getByRole('button', { name: 'Проверить результат', exact: true }).click(); await page.getByRole('button', { name: 'Проверить результат', exact: true }).waitFor({ state: 'hidden' });
  }
  await card(name).waitFor(); assert.equal(writes.filter(write => write.path === '/bff/core/roles' && write.method === 'POST').length, before + 1);
  const found = (await read('/core/roles')).roles.filter(role => role.name === name); assert.equal(found.length, 1); return found[0].id;
}
async function mutations() {
  await login(); await switchDivision(demo.division_a); await openAccess();
  let pending;
  if (!process.argv.includes('--resume-users')) {
  demo.editor_role_name = `${demo.prefix} editor`; demo.editor_role_id = await createRole(demo.editor_role_name); ok('existing role matrix writes owner sections, normalizes edit/view and has no unsupported description');
  demo.recovered_role_name = `${demo.prefix} recovered`; demo.recovered_role_id = await createRole(demo.recovered_role_name, true); ok('lost role POST recovers after reload with exactly one write and no password journal');
  await card(demo.editor_role_name).getByRole('button', { name: 'Настроить', exact: true }).click(); await page.getByLabel('Группы: просмотр', { exact: true }).check();
  const switchBefore = await page.evaluate(() => performance.getEntriesByType('resource').filter(entry => entry.name.includes('/bff/auth/context')).length);
  await page.getByLabel('Переключить кабинет и подразделение').selectOption(`${demo.organization_id}:${demo.division_b}`); await page.getByText(/Закройте редактор/).waitFor();
  assert.equal(await page.evaluate(() => performance.getEntriesByType('resource').filter(entry => entry.name.includes('/bff/auth/context')).length), switchBefore);
  pending = responseFor(`/bff/core/roles/${demo.editor_role_id}`, 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await dialog().waitFor({ state: 'hidden' });
  const edited = (await read('/core/roles')).roles.find(role => role.id === demo.editor_role_id); assert(edited.permissions.some(permission => permission.section === 'groups' && permission.can_view)); ok('role edit preserves complete permissions and open editor blocks cabinet switch');
  }
  await usersTab(); await page.getByRole('button', { name: '+ Добавить пользователя', exact: true }).click();
  const fullName = `${demo.prefix} employee`, loginName = `access_${demo.run}_employee`;
  await page.getByLabel('ФИО', { exact: true }).fill(fullName); await page.getByLabel('Логин', { exact: true }).fill(loginName); await dialog().getByRole('combobox').selectOption(String(demo.editor_role_id)); await page.getByLabel('Пароль', { exact: true }).fill(password);
  pending = responseFor('/bff/core/users', 'POST'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await page.getByText('Учётные данные сохранены', { exact: true }).waitFor(); await page.getByRole('button', { name: 'Закрыть', exact: true }).click();
  const created = (await staff()).find(user => user.login === loginName); assert(created); assert.notEqual(created.account_id, created.profile_id); assert.equal(await page.getByRole('columnheader', { name: 'Пароль', exact: true }).count(), 0);
  demo.employee = { account_id: created.account_id, full_name: fullName, login: loginName }; ok('access workspace creates selected staff with distinct account identity and immediate-only credentials');
  pending = responseFor(`/bff/core/memberships/${created.account_id}`, 'DELETE'); await row(fullName).getByRole('button', { name: 'Отозвать доступ', exact: true }).click(); assert.equal((await pending).status(), 409); await page.getByText(/Нельзя отозвать последнее участие/).waitFor(); assert((await staff()).some(user => user.account_id === created.account_id)); ok('revoke labels membership operation and last-membership conflict retains account');
  const bToken = await token(demo.division_b); await owner(bToken, `/v1/core/divisions/${demo.division_b}/memberships/${created.account_id}`, 'PUT', { role_id: demo.editor_role_id });
  await row(fullName).getByRole('button', { name: 'Изменить', exact: true }).click(); await page.getByLabel('Логин', { exact: true }).fill(`${loginName}_new`); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); await page.getByText('Для смены логина укажите новый пароль', { exact: true }).waitFor();
  await page.getByLabel('Новый пароль', { exact: true }).fill(password); await dialog().getByRole('combobox').selectOption(String(demo.recovered_role_id)); await page.getByLabel('ФИО', { exact: true }).fill(`${fullName} changed`);
  pending = responseFor(`/bff/core/memberships/${created.account_id}`, 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await page.getByText('Учётные данные сохранены', { exact: true }).waitFor(); await page.getByRole('button', { name: 'Закрыть', exact: true }).click();
  demo.employee.login = `${loginName}_new`; demo.employee.full_name = `${fullName} changed`; await row(demo.employee.full_name).waitFor(); const ownerUser = await read(`/core/users/${created.account_id}`); assert.equal(ownerUser.login, demo.employee.login); assert.equal(ownerUser.role_id, demo.recovered_role_id); ok('user edit validates changed login then updates fresh profile, credential PUT and selected membership role');
  await row(demo.employee.full_name).getByRole('button', { name: 'Изменить', exact: true }).click(); await page.getByLabel('Аккаунт активен', { exact: true }).uncheck(); pending = responseFor(`/bff/core/users/${created.account_id}/active`, 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await row(demo.employee.full_name).getByText('Отключён', { exact: true }).waitFor();
  await row(demo.employee.full_name).getByRole('button', { name: 'Изменить', exact: true }).click(); await page.getByLabel('Аккаунт активен', { exact: true }).check(); pending = responseFor(`/bff/core/users/${created.account_id}/active`, 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await row(demo.employee.full_name).getByText('Активен', { exact: true }).waitFor(); ok('activation toggles preserve the staff account, profile and memberships');
  await rolesTab(); await card(demo.editor_role_name).getByText(/Сотрудников в подразделении: 0/).waitFor(); pending = responseFor(`/bff/core/roles/${demo.editor_role_id}`, 'DELETE'); await card(demo.editor_role_name).getByRole('button', { name: 'Удалить', exact: true }).click(); assert.equal((await pending).status(), 409); await page.getByText(/Роль используется в одном или нескольких подразделениях/).waitFor(); ok('zero selected assignments is honest and owner prevents deletion of a role still used in another division');
  await usersTab(); pending = responseFor(`/bff/core/memberships/${created.account_id}`, 'DELETE'); await row(demo.employee.full_name).getByRole('button', { name: 'Отозвать доступ', exact: true }).click(); assert((await pending).ok()); await row(demo.employee.full_name).waitFor({ state: 'hidden' });
  await switchDivision(demo.division_b); await openAccess(); await usersTab(); await row(demo.employee.full_name).waitFor(); const retained = await read(`/core/users/${created.account_id}`); assert.equal(retained.role_id, demo.editor_role_id); assert(retained.is_active); ok('revoking A access preserves the active account and independently assigned B role');
  await switchDivision(demo.division_a); await openAccess(); await rolesTab(); pending = responseFor(`/bff/core/roles/${demo.recovered_role_id}`, 'DELETE'); await card(demo.recovered_role_name).getByRole('button', { name: 'Удалить', exact: true }).click(); assert((await pending).ok()); await card(demo.recovered_role_name).waitFor({ state: 'hidden' }); demo.recovered_role_deleted = true; ok('owner deletes an unused organization role without deleting staff');
  await fs.writeFile(manifest, JSON.stringify(demo, null, 2));
}
async function readonlyChecks() {
  await login(); await switchDivision(demo.division_a); await openAccess(); await card(demo.editor_role_name).waitFor(); await card(demo.editor_role_name).getByText(/Сотрудников в подразделении: 0/).waitFor();
  await usersTab(); await row(`${demo.prefix} viewer`).waitFor(); assert.equal(await row(demo.employee.full_name).count(), 0); assert.equal(await row(demo.foreign.full_name).count(), 0); ok('full admin Access roles are organization scoped while staff counts and roster use selected division');
  await switchDivision(demo.division_b); await openAccess(); await card(demo.editor_role_name).getByText(/Сотрудников в подразделении: 1/).waitFor(); await usersTab(); await row(demo.employee.full_name).waitFor(); await row(demo.foreign.full_name).waitFor(); assert.equal(await page.getByRole('columnheader', { name: 'Пароль', exact: true }).count(), 0); ok('cabinet switch shows B assignments with same organization roles, active status and no password table');
  await rolesTab(); await card(demo.editor_role_name).getByRole('button', { name: 'Настроить', exact: true }).click(); await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForFunction(() => getComputedStyle(document.querySelector('.cabinet-main')).marginLeft === '0px');
  if (process.argv.includes('--diagnose-mobile')) console.log(JSON.stringify(await page.evaluate(() => ({ viewport: innerWidth, documentWidth: document.documentElement.scrollWidth, overflow: [...document.querySelectorAll('body *')].filter(node => { const bounds = node.getBoundingClientRect(); return bounds.left >= 0 && bounds.right > innerWidth + 1; }).slice(0, 15).map(node => ({ tag: node.tagName, class: node.className, bounds: { left: node.getBoundingClientRect().left, right: node.getBoundingClientRect().right, width: node.getBoundingClientRect().width } })) }))));
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await page.screenshot({ path: root + 'dev/local/bff-core-access-role-mobile.png' }); await dialog().getByRole('button', { name: 'Закрыть', exact: true }).click();
  await usersTab(); await page.evaluate(() => scrollTo(0, 0)); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await page.screenshot({ path: root + 'dev/local/bff-core-access-users-mobile.png' }); ok('existing role matrix and staff roster fit 390px without document overflow');
  await login(demo.viewer.login); assert.equal(await page.getByRole('link', { name: 'Доступ и роли', exact: true }).count(), 0); const response = await context.request.get(base + '/bff/core/roles', { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': revision } }); assert.equal(response.status(), 403); ok('delegated staff cannot enter Access or read organization roles even with an authorized user roster');
  assert.deepEqual(errors, []); assert.deepEqual(external, []); assert.deepEqual(legacy, []); if (readOnly) assert.deepEqual(writes, []); ok(readOnly ? 'optimized Access readonly has zero JS, legacy/external requests and business writes' : 'Access owner flows have zero JS, legacy fallback and external requests');
}
try {
  if (readOnly || process.argv.includes('--prepared') || process.argv.includes('--resume-users')) demo = JSON.parse(await fs.readFile(manifest, 'utf8')); else await prepare();
  if (process.argv.includes('--resume-users')) checks.push(...JSON.parse(await fs.readFile(root + 'dev/local/bff-core-access-crud-browser-report.json', 'utf8')).checks);
  if (process.argv.includes('--prepare')) ok('fresh Access fixtures created via public Core API'); else { if (!readOnly) await mutations(); await readonlyChecks(); }
} catch (error) {
  console.error(error.stack);
  if (page) { await page.screenshot({ path: root + 'dev/local/bff-core-access-failure.png', fullPage: true, mask: [page.locator('[class*="credentials"]'), page.locator('input[type="password"]')] }); }
  throw error;
} finally {
  if (demo && !readOnly) await fs.writeFile(manifest, JSON.stringify(demo, null, 2));
  await fs.writeFile(root + `dev/local/bff-core-access-${readOnly ? 'optimized' : 'crud'}-browser-report.json`, JSON.stringify({ run: demo?.run, checks, errors, external, legacy, writes, readOnly }, null, 2));
  await browser.close();
}
