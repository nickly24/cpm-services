/** Organization UI via same-origin BFF only. Default uses fresh fixtures; --read-only never writes business data. */
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const root = new URL('../../../', import.meta.url).pathname, base = 'http://127.0.0.1:3001';
const readOnly = process.argv.includes('--read-only'), renameOrganization = process.argv.includes('--rename-org');
const { password } = JSON.parse(await fs.readFile(root + 'dev/local/accounts.json', 'utf8'));
const manifest = root + 'dev/local/bff-core-organization-demo.json';
const checks = [], errors = [], external = [], legacy = [], writes = [];
let demo, context, page, revision, cabinet = 'admin', stage = 'browser', restoreOrganizationName = null;
const browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
const ok = name => { checks.push(name); console.log(JSON.stringify({ check: name })); };
const saveManifest = async () => { if (demo && !readOnly) await fs.writeFile(manifest, JSON.stringify(demo, null, 2)); };
function responseFor(path, method = 'GET') { const promise = page.waitForResponse(response => new URL(response.url()).pathname === path && response.request().method() === method); void promise.catch(() => {}); return promise; }
async function login(loginName = 'dev_admin') {
  if (context) await context.close();
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true });
  await context.route('**/*', route => {
    const request = route.request(), url = new URL(request.url());
    if (url.origin !== base) { external.push(url.origin + url.pathname); return route.abort(); }
    if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/bff/unconnected')) legacy.push(url.pathname);
    if (readOnly && !['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(url.pathname)) { writes.push({ path: url.pathname, method: request.method(), stage }); return route.abort(); }
    return route.continue();
  });
  page = await context.newPage(); page.setDefaultTimeout(20000); page.on('pageerror', error => errors.push(error.message)); page.on('dialog', dialog => dialog.accept());
  page.on('request', request => { const path = new URL(request.url()).pathname; if (!['GET', 'HEAD'].includes(request.method()) && !['/bff/auth/login', '/bff/auth/context'].includes(path)) writes.push({ path, method: request.method(), stage }); });
  await page.goto(base + '/login'); await page.getByLabel('Логин', { exact: true }).fill(loginName); await page.getByLabel('Пароль', { exact: true }).fill(password);
  const pending = responseFor('/bff/auth/login', 'POST'); await page.getByRole('button', { name: 'Войти', exact: true }).click(); const result = await (await pending).json(); assert(result.status); revision = result.context_revision; cabinet = result.user.role;
  await page.waitForURL('**/cabinet/**'); await page.waitForLoadState('networkidle');
}
async function call(path, method = 'GET', body, expected = 200) {
  assert(!readOnly || method === 'GET', 'Readonly fixture helper refuses all business writes');
  if (method !== 'GET') writes.push({ path: '/bff' + path, method, stage });
  const response = await context.request.fetch(base + '/bff' + path, { method, headers: { Origin: base, 'X-CPM-Request': '1', 'X-CPM-Context': revision }, ...(body === undefined ? {} : { data: body }) });
  assert.equal(response.status(), expected, `${method} ${path}`); const result = await response.json(); return result.data;
}
async function switchDivision(division) {
  const select = page.getByLabel('Переключить кабинет и подразделение'); await select.waitFor(); const value = `${demo.organization_id}:${division}`;
  if (await select.inputValue() !== value) { const loaded = page.waitForEvent('load'); void loaded.catch(() => {}); const pending = responseFor('/bff/auth/context', 'POST'); await select.selectOption(value); assert((await pending).ok()); await loaded; await page.waitForLoadState('networkidle'); }
  revision = (await (await context.request.get(base + '/bff/auth/me', { headers: { 'X-CPM-Request': '1' } })).json()).context_revision;
}
async function prepare() {
  stage = 'fixture'; await login();
  const run = Date.now().toString(36), prefix = `DEV Org ${run}`;
  demo = { run, prefix, organization_id: 1001 };
  demo.division_a = (await call('/core/divisions', 'POST', { name: prefix + ' A' }, 201)).id;
  demo.division_b = (await call('/core/divisions', 'POST', { name: prefix + ' B' }, 201)).id;
  const permissions = { users: { view: true, edit: false }, organizations: { view: true, edit: true }, divisions: { view: true, edit: true }, directions: { view: true, edit: true } };
  demo.role_a = (await call('/core/roles', 'POST', { name: prefix + ' role A', permissions }, 201)).id;
  demo.role_b = (await call('/core/roles', 'POST', { name: prefix + ' role B', permissions: { users: { view: true, edit: true } } }, 201)).id;
  await page.reload(); await page.waitForLoadState('networkidle'); await switchDivision(demo.division_a);
  demo.student = { full_name: prefix + ' student', login: `org_${run}_student` };
  demo.staff = { full_name: prefix + ' staff', login: `org_${run}_staff` };
  demo.viewer = { full_name: prefix + ' viewer', login: `org_${run}_viewer` };
  for (const [key, role] of [['student', 'student'], ['staff', 'staff_admin'], ['viewer', 'staff_admin']]) {
    const created = await call('/core/users', 'POST', { ...demo[key], password, role, class: role === 'student' ? 10 : 0, ...(role === 'staff_admin' ? { role_id: demo.role_a } : {}) }, 201);
    demo[key].account_id = created.account_id; demo[key].profile_id = created.profile_id;
  }
  demo.group_a = (await call('/core/groups', 'POST', { name: prefix + ' group A' }, 201)).id;
  await call(`/core/groups/${demo.group_a}/members`, 'POST', { account_id: demo.student.account_id });
  await saveManifest(); stage = 'browser';
}
async function open(section = 'organizations') {
  await page.goto(`${base}/cabinet/${cabinet}/${section}`); await page.getByRole('heading', { name: section === 'organizations' ? 'Организация' : section === 'divisions' ? 'Подразделения' : 'Направления', exact: true }).waitFor();
  await page.getByText('Загрузка организации…', { exact: true }).waitFor({ state: 'hidden' }); await page.waitForLoadState('networkidle');
}
const dialog = () => page.getByRole('dialog');
const card = name => page.locator('article').filter({ has: page.getByRole('heading', { name, exact: true }) });
const panel = () => page.getByRole('region', { name: 'Участия выбранного аккаунта' });
const row = name => page.locator('tbody tr').filter({ hasText: name });
async function membersTab() { await page.getByRole('button', { name: 'Участия аккаунтов', exact: true }).click(); await page.getByLabel('Найти существующий аккаунт').fill(demo.run); await row(demo.student.full_name).waitFor(); }
async function selectAccount(account) { const pending = responseFor(`/bff/core/organization/users/${account.account_id}/memberships`); await row(account.full_name).getByRole('button').click(); assert((await pending).ok()); await panel().getByRole('heading', { name: account.full_name, exact: true }).waitFor(); await panel().getByText('Проверка участий аккаунта…', { exact: true }).waitFor({ state: 'hidden' }); }
const details = account => call(`/core/organization/users/${account.account_id}/memberships`);
async function addParticipation(account, division, role = null) {
  await selectAccount(account); await panel().getByRole('button', { name: 'Добавить участие', exact: true }).click(); await dialog().getByLabel('Подразделение').selectOption(String(division));
  if (role !== null) await dialog().getByLabel('Административная роль').selectOption(String(role));
  const pending = responseFor(`/bff/core/divisions/${division}/memberships/${account.account_id}`, 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await dialog().waitFor({ state: 'hidden' }); await page.waitForLoadState('networkidle');
}
async function changeRole(division, role, loseResponse = false) {
  await selectAccount(demo.staff); await card(demo.prefix + (division === demo.division_a ? ' A' : ' B')).getByRole('button', { name: 'Изменить роль', exact: true }).click();
  await dialog().getByLabel('Административная роль').selectOption(String(role));
  const path = `/bff/core/divisions/${division}/memberships/${demo.staff.account_id}`, count = writes.filter(write => write.path === path && write.method === 'PUT').length;
  if (loseResponse) {
    let intercepted = false;
    await page.route('**' + path, async route => { if (route.request().method() !== 'PUT' || intercepted) return route.continue(); intercepted = true; assert((await route.fetch()).ok()); await route.abort('failed'); });
  }
  const pending = loseResponse ? null : responseFor(path, 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click();
  if (pending) { assert((await pending).ok()); await dialog().waitFor({ state: 'hidden' }); }
  else {
    await page.getByRole('button', { name: 'Проверить результат', exact: true }).waitFor(); await page.reload(); await page.getByRole('button', { name: 'Проверить результат', exact: true }).waitFor();
    assert.equal(await page.evaluate(secret => Object.values(sessionStorage).some(value => value.includes(secret)), password), false);
    await page.getByRole('button', { name: 'Проверить результат', exact: true }).click(); await page.getByRole('button', { name: 'Проверить результат', exact: true }).waitFor({ state: 'hidden' }); await membersTab();
  }
  assert.equal(writes.filter(write => write.path === path && write.method === 'PUT').length, count + 1);
}
async function catalogCrud() {
  await open('divisions'); demo.created_division_name = demo.prefix + ' created';
  await page.getByRole('button', { name: '+ Создать подразделение', exact: true }).click(); await dialog().getByLabel('Название', { exact: true }).fill(demo.created_division_name);
  let lost = false; const before = writes.filter(write => write.path === '/bff/core/divisions' && write.method === 'POST').length;
  await page.route('**/bff/core/divisions', async route => { if (route.request().method() !== 'POST' || lost) return route.continue(); lost = true; assert((await route.fetch()).ok()); await route.abort('failed'); });
  await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); await page.getByRole('button', { name: 'Проверить результат', exact: true }).waitFor();
  await page.reload(); await page.getByRole('button', { name: 'Проверить результат', exact: true }).click(); await page.getByRole('button', { name: 'Проверить результат', exact: true }).waitFor({ state: 'hidden' }); await card(demo.created_division_name).waitFor();
  assert.equal(writes.filter(write => write.path === '/bff/core/divisions' && write.method === 'POST').length, before + 1);
  demo.created_division = (await call('/core/divisions')).find(item => item.name === demo.created_division_name).id; ok('lost division create confirms one new owner ID after reload without repeating POST');
  await card(demo.created_division_name).getByRole('button', { name: 'Переименовать', exact: true }).click(); demo.renamed_division_name = demo.prefix + ' renamed'; await dialog().getByLabel('Название', { exact: true }).fill(demo.renamed_division_name);
  let pending = responseFor(`/bff/core/divisions/${demo.created_division}`, 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await card(demo.renamed_division_name).waitFor();
  pending = responseFor(`/bff/core/divisions/${demo.created_division}`, 'DELETE'); await card(demo.renamed_division_name).getByRole('button', { name: 'Архивировать', exact: true }).click(); assert((await pending).ok()); await card(demo.renamed_division_name).waitFor({ state: 'hidden' }); ok('own empty division can rename and archive without deleting users/history');
  pending = responseFor(`/bff/core/divisions/${demo.division_a}`, 'DELETE'); await card(demo.prefix + ' A').getByRole('button', { name: 'Архивировать', exact: true }).click(); assert.equal((await pending).status(), 409); await page.getByText(/В подразделении есть участники/).waitFor(); ok('owner rejects archiving a division with members and keeps its cabinet active');
  await open('directions'); demo.direction_name = demo.prefix + ' direction'; await page.getByRole('button', { name: '+ Создать направление', exact: true }).click(); await dialog().getByLabel('Название', { exact: true }).fill(demo.direction_name);
  pending = responseFor('/bff/core/directions', 'POST'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await card(demo.direction_name).waitFor(); demo.direction_id = (await call('/core/directions')).find(item => item.name === demo.direction_name).id;
  await card(demo.direction_name).getByRole('button', { name: 'Переименовать', exact: true }).click(); demo.direction_renamed = demo.prefix + ' direction changed'; await dialog().getByLabel('Название', { exact: true }).fill(demo.direction_renamed);
  pending = responseFor(`/bff/core/directions/${demo.direction_id}`, 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await card(demo.direction_renamed).waitFor();
  pending = responseFor(`/bff/core/directions/${demo.direction_id}`, 'DELETE'); await card(demo.direction_renamed).getByRole('button', { name: 'Архивировать', exact: true }).click(); assert((await pending).ok()); await card(demo.direction_renamed).waitFor({ state: 'hidden' }); ok('organization direction create/rename/archive uses current owner catalog'); await saveManifest();
}
async function membershipCrud() {
  await open(); await membersTab(); await selectAccount(demo.student);
  let pending;
  if (!checks.includes('last membership rejection preserves the existing account')) { pending = responseFor(`/bff/core/divisions/${demo.division_a}/memberships/${demo.student.account_id}`, 'DELETE'); await card(demo.prefix + ' A').getByRole('button', { name: 'Снять участие', exact: true }).click(); assert.equal((await pending).status(), 409); await page.getByText(/Нельзя снять последнее участие/).waitFor(); ok('last membership rejection preserves the existing account'); }
  if (!(await details(demo.student)).memberships.some(item => item.division_id === demo.division_b)) await addParticipation(demo.student, demo.division_b); let dto = await details(demo.student); assert.equal(dto.memberships.length, 2); assert.equal(dto.memberships.find(item => item.division_id === demo.division_a).group_id, demo.group_a); assert.equal(dto.memberships.find(item => item.division_id === demo.division_b).group_id, null); ok('existing account gains B participation while its A group/profile remain unchanged');
  if (!(await details(demo.staff)).memberships.some(item => item.division_id === demo.division_b)) await addParticipation(demo.staff, demo.division_b, demo.role_b); dto = await details(demo.staff); assert.equal(dto.memberships.find(item => item.division_id === demo.division_a).role_id, demo.role_a); assert.equal(dto.memberships.find(item => item.division_id === demo.division_b).role_id, demo.role_b); ok('staff roles are displayed and assigned independently for A and B participation');
  await changeRole(demo.division_a, demo.role_b, true); dto = await details(demo.staff); assert.equal(dto.memberships.find(item => item.division_id === demo.division_a).role_id, demo.role_b); ok('lost participation role PUT recovers by canonical GET after reload with one write and no secrets');
  await changeRole(demo.division_a, demo.role_a); await selectAccount(demo.student);
  await panel().getByRole('button', { name: 'Добавить участие', exact: true }).click();
  const available = await dialog().getByLabel('Подразделение').locator('option').evaluateAll(options => options.map(option => option.value));
  assert(!available.includes(String(demo.division_a))); assert(!available.includes(String(demo.division_b))); await dialog().getByRole('button', { name: 'Отмена', exact: true }).click();
  await selectAccount(demo.staff); await card(demo.prefix + ' A').getByRole('button', { name: 'Изменить роль', exact: true }).click(); await dialog().waitFor();
  await page.getByLabel('Переключить кабинет и подразделение').selectOption(`${demo.organization_id}:${demo.division_b}`); await page.getByText(/Закройте редактор/).waitFor(); await dialog().getByRole('button', { name: 'Отмена', exact: true }).click(); ok('open participation editor blocks cabinet switch before context transport');
  await selectAccount(demo.student); pending = responseFor(`/bff/core/divisions/${demo.division_a}/memberships/${demo.student.account_id}`, 'DELETE'); await card(demo.prefix + ' A').getByRole('button', { name: 'Снять участие', exact: true }).click(); assert((await pending).ok());
  dto = await details(demo.student); assert.deepEqual(dto.memberships.map(item => item.division_id), [demo.division_b]); await addParticipation(demo.student, demo.division_a); ok('revoke removes only A membership and owner account can be found to add it back'); await saveManifest();
}
async function metadataRename() {
  assert(!readOnly); await open(); restoreOrganizationName = (await call('/core/organization')).name;
  await page.getByRole('button', { name: 'Переименовать организацию', exact: true }).click(); const name = demo.prefix + ' temporary'; await dialog().getByLabel('Название', { exact: true }).fill(name);
  let pending = responseFor('/bff/core/organization', 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); await card(name).waitFor();
  await page.getByRole('button', { name: 'Переименовать организацию', exact: true }).click(); await dialog().getByLabel('Название', { exact: true }).fill(restoreOrganizationName); pending = responseFor('/bff/core/organization', 'PUT'); await dialog().getByRole('button', { name: 'Сохранить', exact: true }).click(); assert((await pending).ok()); restoreOrganizationName = null; ok('current dev organization rename and restoration verified in authorized metadata window');
}
async function readonlyChecks() {
  await login(); await switchDivision(demo.division_a); await open(); await membersTab(); await selectAccount(demo.staff); const dto = await details(demo.staff);
  assert.equal(dto.memberships.find(item => item.division_id === demo.division_a).role_id, demo.role_a); assert.equal(dto.memberships.find(item => item.division_id === demo.division_b).role_id, demo.role_b);
  for (const suffix of [' A', ' B']) await card(demo.prefix + suffix).getByRole('button', { name: 'Изменить роль', exact: true }).waitFor();
  const profile = await call(`/core/users/${demo.staff.account_id}`); assert.equal(profile.account_id, demo.staff.account_id); assert(Number.isSafeInteger(profile.profile_id)); assert.notEqual(profile.account_id, profile.profile_id); ok('full admin sees real per-division roles and distinct auth account identity');
  assert(!(await call('/core/divisions')).some(item => item.id === demo.created_division)); assert(!(await call('/core/directions')).some(item => item.id === demo.direction_id)); ok('archived own division/direction stay absent from live active catalogs');
  await page.setViewportSize({ width: 390, height: 844 }); await page.waitForFunction(() => getComputedStyle(document.querySelector('.cabinet-main')).marginLeft === '0px'); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await page.screenshot({ path: root + 'dev/local/bff-core-organization-members-mobile.png' }); assert(await page.getByLabel('Переключить кабинет и подразделение').count()); ok('organization tabs/account table/participations fit390px with cabinet switch available');
  await page.setViewportSize({ width: 1440, height: 1000 }); await switchDivision(demo.division_b); await open('divisions'); await card(demo.prefix + ' B').getByText('Выбранный кабинет', { exact: true }).waitFor(); ok('header cabinet switch reloads matching current division');
  await login(demo.viewer.login); await open('divisions'); await card(demo.prefix + ' A').waitFor(); assert.equal(await card(demo.prefix + ' B').count(), 0); assert.equal(await page.getByRole('button', { name: 'Участия аккаунтов', exact: true }).count(), 0); assert.equal(await page.getByRole('button', { name: /Создать|Переименовать|Архивировать/ }).count(), 0); ok('delegated staff sees only its accessible divisions and no organization write/member controls');
  const denied = await context.request.get(base + `/bff/core/organization/users/${demo.student.account_id}/memberships`, { headers: { 'X-CPM-Request': '1', 'X-CPM-Context': revision } }); assert.equal(denied.status(), 403); ok('live owner denies staff organization-wide account participation endpoint');
  await open('directions'); assert.equal(await page.getByRole('button', { name: '+ Создать направление', exact: true }).count(), 0); await open(); assert.equal(await page.getByRole('button', { name: 'Переименовать организацию', exact: true }).count(), 0); ok('owner-readable organization/direction catalogs stay readonly for scoped staff');
  assert.deepEqual(errors, []); assert.deepEqual(external, []); assert.deepEqual(legacy, []); if (readOnly) assert.deepEqual(writes, []); ok(readOnly ? 'optimized readonly has zero business writes, JS errors, external/owner URLs or legacy calls' : 'organization UI uses BFF only with zero JS errors, external/owner URLs or legacy fallback');
}
try {
  if (readOnly || process.argv.includes('--prepared') || process.argv.includes('--memberships')) demo = JSON.parse(await fs.readFile(manifest, 'utf8')); else await prepare();
  if (process.argv.includes('--prepare')) ok('fresh organization fixtures prepared through public same-origin BFF');
  else { if (!readOnly) { if (process.argv.includes('--prepared') || process.argv.includes('--memberships')) { await login(); await switchDivision(demo.division_a); } if (process.argv.includes('--memberships')) checks.push(...JSON.parse(await fs.readFile(root + 'dev/local/bff-core-organization-crud-browser-report.json', 'utf8')).checks); else await catalogCrud(); await membershipCrud(); if (renameOrganization) await metadataRename(); } await readonlyChecks(); }
} catch (error) {
  console.error(error.stack); if (page) await page.screenshot({ path: root + 'dev/local/bff-core-organization-failure.png', fullPage: true, mask: [page.locator('input[type=password]')] }); throw error;
} finally {
  if (restoreOrganizationName !== null) { try { await login(); await call('/core/organization', 'PUT', { name: restoreOrganizationName }); } catch { console.error('Organization restore requires parent attention'); } }
  await saveManifest(); await fs.writeFile(root + `dev/local/bff-core-organization-${readOnly ? 'optimized' : 'crud'}-browser-report.json`, JSON.stringify({ run: demo?.run, checks, errors, external, legacy, writes, readOnly }, null, 2)); await browser.close();
}
