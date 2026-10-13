/** Narrow read-only Core report proof on retained fresh public-API fixtures. */
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { chromium } from "/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs";
import * as XLSX from "../../../cpm_front_new/node_modules/xlsx/xlsx.mjs";

const root = new URL("../../../", import.meta.url).pathname;
const output = `${root}dev/local`;
const origin = "http://127.0.0.1:3001";
const manifest = JSON.parse(await fs.readFile(`${output}/core-external-report-demo.json`, "utf8"));
const { password } = JSON.parse(await fs.readFile(`${output}/accounts.json`, "utf8"));
assert.equal(manifest.readiness, "ready");
assert.ok(manifest.prefix.startsWith("DEV Report "));
assert.ok(!manifest.commands.some(command => command.status === "pending"));
const reportPath = `${output}/core-external-report-browser-report.json`;
const checks = [], errors = [], external = [], requests = [];
let contextRevision = "", actualReport, browser, context, page, reportRequestStart = 0;
const pass = label => { checks.push(label); console.log(`PASS ${label}`); };
const save = async (path, value) => { await fs.writeFile(path, JSON.stringify(value, null, 2)); await fs.chmod(path, 0o600); };
const actor = manifest.actors.supervisor;

try {
  browser = await chromium.launch({ executablePath: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", headless: true });
  context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true, serviceWorkers: "block" });
  await context.route("**/*", route => {
    const url = new URL(route.request().url());
    if (url.hostname !== "127.0.0.1") { external.push(`${url.protocol}//${url.hostname}${url.pathname}`); return route.abort(); }
    if (url.pathname.startsWith("/bff/") && !["GET", "HEAD"].includes(route.request().method()) && !url.pathname.startsWith("/bff/auth/")) throw new Error("Read-only report browser attempted a subject write");
    return route.continue();
  });
  page = await context.newPage(); page.setDefaultTimeout(20000);
  page.on("pageerror", error => errors.push(error.message));
  page.on("request", request => {
    const url = new URL(request.url());
    if (!url.pathname.startsWith("/bff/")) return;
    assert.equal(request.headers().authorization, undefined, "Browser acquired bearer credentials");
    contextRevision = request.headers()["x-cpm-context"] || contextRevision;
    requests.push({ method: request.method(), path: url.pathname, query: url.search });
  });
  await page.goto(`${origin}/login`);
  await page.getByLabel("Логин", { exact: true }).fill(actor.login);
  await page.getByLabel("Пароль", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await page.waitForURL("**/cabinet/**");
  const selector = page.getByLabel("Переключить кабинет и подразделение");
  const option = await selector.locator("option").evaluateAll((options, division) => options.find(item => item.value.endsWith(`:${division}`))?.value, manifest.division_a);
  assert.ok(option); if (await selector.inputValue() !== option) await selector.selectOption(option);
  const cookie = (await context.cookies()).find(item => item.name === "cpm_bff");
  assert.ok(cookie?.httpOnly && cookie.sameSite === "Strict");
  pass("fresh supervisor selects only its synthetic division with HttpOnly cookie");

  reportRequestStart = requests.length;
  await page.goto(`${origin}/cabinet/supervisor/tests`);
  await page.getByRole("button", { name: "Диапазон дат", exact: true }).click();
  await page.locator("#rep-from").fill(manifest.period.date_from);
  await page.locator("#rep-to").fill(manifest.period.date_to);
  const responsePromise = page.waitForResponse(response => new URL(response.url()).pathname === "/bff/core/reports/tests" && response.status() === 200);
  await page.getByRole("button", { name: "Создать", exact: true }).click();
  const response = await responsePromise;
  const envelope = await response.json(); actualReport = envelope.data;
  assert.equal(actualReport.organization_id, manifest.organization_id);
  assert.equal(actualReport.division_id, manifest.division_a);
  assert.deepEqual(actualReport.items, manifest.report.items);
  assert.deepEqual(actualReport.cells, manifest.report.cells);
  assert.deepEqual(actualReport.students, manifest.report.students);
  assert.equal(actualReport.date_from, manifest.period.date_from);
  assert.equal(actualReport.date_to, manifest.period.date_to);
  await page.getByRole("heading", { name: "Результаты тестов", exact: true }).waitFor();
  assert.equal(await page.locator("tbody tr").count(), 2);
  pass("UI reads complete two-account by three-target native report through selected BFF context");

  const own = page.locator("tbody tr").filter({ has: page.getByRole("rowheader", { name: manifest.actors.student.full_name, exact: true }) });
  const peer = page.locator("tbody tr").filter({ has: page.getByRole("rowheader", { name: manifest.actors.peer.full_name, exact: true }) });
  assert.equal(await own.locator('td[data-status="ready"]').getByText("0", { exact: true }).count(), 1);
  assert.equal(await own.getByText("Без оценки", { exact: true }).count(), 1);
  assert.equal(await own.getByText("Нет результата", { exact: true }).count(), 1);
  assert.equal(await peer.getByText("Нет результата", { exact: true }).count(), 3);
  assert.equal(await peer.locator('td[data-status="ready"]').count(), 0);
  pass("actual zero, recorded null and absent result have three distinct labels without invented peer grades");
  assert.equal(await page.locator("thead small").filter({ hasText: "Внешний тест" }).count(), 3);
  for (const target of Object.values(manifest.tests)) {
    const header = page.locator("thead th").filter({ hasText: target.name });
    assert.equal(await header.count(), 1);
    assert.equal(await header.locator("small").textContent(), "2031-04-05 · Внешний тест");
  }
  assert.equal(manifest.actors.student.account_id, actualReport.students.find(item => item.full_name === manifest.actors.student.full_name).account_id);
  assert.equal(manifest.actors.student.profile_id, actualReport.students.find(item => item.full_name === manifest.actors.student.full_name).profile_id);
  assert.notEqual(manifest.actors.student.account_id, manifest.actors.student.profile_id);
  pass("three external badges retain real names, date and distinct account/profile identities");

  await page.getByRole("combobox", { name: "Класс", exact: true }).selectOption("7");
  assert.equal(await page.locator("tbody tr").count(), 1);
  assert.equal(await own.count(), 1);
  await page.getByRole("combobox", { name: "Класс", exact: true }).selectOption("");
  await page.getByLabel("Поиск ученика", { exact: true }).fill(manifest.actors.peer.full_name);
  assert.equal(await page.locator("tbody tr").count(), 1);
  assert.equal(await peer.count(), 1);
  await page.getByLabel("Поиск ученика", { exact: true }).fill("");
  await page.getByRole("combobox", { name: "Группа", exact: true }).selectOption("none");
  await page.getByText("Ученики по выбранным фильтрам не найдены.", { exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "Скачать Excel", exact: true }).isDisabled(), true);
  await page.getByRole("combobox", { name: "Группа", exact: true }).selectOption(String(manifest.groups.a));
  assert.equal(await page.locator("tbody tr").count(), 2);
  await page.getByRole("combobox", { name: "Группа", exact: true }).selectOption("");
  await page.getByRole("combobox", { name: "Направление", exact: true }).selectOption(String(manifest.direction_id));
  assert.equal(await page.locator("thead small").count(), 3);
  pass("class, search, group and direction filters preserve true rows and disable an empty export");

  const downloadPromise = page.waitForEvent("download");
  await page.getByRole("button", { name: "Скачать Excel", exact: true }).click();
  const download = await downloadPromise;
  assert.match(download.suggestedFilename(), /\.xlsx$/);
  const file = `${output}/core-external-report.xlsx`;
  await download.saveAs(file); await fs.chmod(file, 0o600);
  const workbook = XLSX.read(await fs.readFile(file), { type: "buffer" });
  const sheet = XLSX.utils.sheet_to_json(workbook.Sheets[workbook.SheetNames[0]], { header: 1 });
  const exported = sheet.find(row => row[0] === manifest.actors.student.full_name);
  const otherExported = sheet.find(row => row[0] === manifest.actors.peer.full_name);
  assert.ok(exported.includes(0) && exported.includes("Без оценки") && exported.includes("Нет результата"));
  assert.equal(otherExported.filter(value => value === "Нет результата").length, 3);
  const exportedHeader = sheet.find(row => row[0] === "Ученик");
  assert.ok(exportedHeader);
  assert.equal(exportedHeader.filter(value => String(value).includes("Внешний тест")).length, 3);
  pass("actual XLSX contains numeric zero and separate recorded-null/absent labels with external headers");

  const refresh = page.waitForResponse(response => new URL(response.url()).pathname === "/bff/core/reports/tests" && response.status() === 200);
  await page.getByRole("button", { name: "Обновить отчёт", exact: true }).click(); await refresh;
  await own.getByText("Без оценки", { exact: true }).waitFor();
  assert.equal(await own.locator('td[data-status="ready"]').getByText("0", { exact: true }).count(), 1);
  pass("fresh report reload keeps zero and explicit-null source state without rating publication");

  assert.ok(contextRevision);
  const denied = await context.request.post(`${origin}/bff/auth/context`, { data: { organization_id: 1001, division_id: 1001 }, headers: { Origin: origin, "X-CPM-Request": "1", "X-CPM-Context": contextRevision } });
  assert.equal(denied.status(), 403);
  const identity = await context.request.get(`${origin}/bff/auth/me`, { headers: { "X-CPM-Request": "1", "X-CPM-Context": contextRevision } });
  assert.equal(identity.status(), 200);
  assert.equal((await identity.json()).user.division_id, manifest.division_a);
  assert.equal(await own.count(), 1);
  pass("foreign division switch is denied and existing selected report remains scoped");

  await page.setViewportSize({ width: 390, height: 844 });
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth), 390);
  await own.getByText("Без оценки", { exact: true }).waitFor();
  const screenshot = `${output}/core-external-report-mobile.png`;
  await page.screenshot({ path: screenshot, fullPage: true }); await fs.chmod(screenshot, 0o600);
  pass("external report stays readable within the mobile viewport with internal table scrolling");
  assert.equal(errors.length, 0); assert.equal(external.length, 0);
  assert.ok(requests.some(request => request.path === "/bff/core/reports/tests" && request.query.includes("date_from=2031-04-05")));
  assert.ok(!requests.slice(reportRequestStart).some(request => request.path.startsWith("/bff/ratings") || request.path.startsWith("/bff/uploads") || request.path.includes("legacy")));
  pass("report workspace makes no source, rating, upload, legacy or external calls and has no console errors");
  await save(reportPath, { status: "passed", count: checks.length, run: manifest.run, checks, errors, external, requests, subject_writes: 0 });
} catch (error) {
  if (page) await page.screenshot({ path: `${output}/core-external-report-browser-failure.png`, fullPage: true }).catch(() => {});
  await save(reportPath, { status: "failed", count: checks.length, run: manifest.run, checks, error: error.message, errors, external, requests, subject_writes: 0 });
  throw error;
} finally {
  if (context && contextRevision) await context.request.post(`${origin}/bff/auth/logout`, { data: {}, headers: { Origin: origin, "X-CPM-Request": "1", "X-CPM-Context": contextRevision } }).catch(() => {});
  if (context) await context.close();
  if (browser) await browser.close();
}
