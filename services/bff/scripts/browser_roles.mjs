import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const root=new URL('../../../',import.meta.url).pathname;
const {password}=JSON.parse(await fs.readFile(root+'dev/local/accounts.json','utf8'));
const fixture=JSON.parse(await fs.readFile(root+'dev/local/bff-access-fixtures.json','utf8'));
const demo=JSON.parse(await fs.readFile(root+'dev/local/bff-demo.json','utf8'));
const browser=await chromium.launch({executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
const checks=[],errors=[],external=[];let page;
async function login(name){
 const ctx=await browser.newContext({viewport:{width:1440,height:1000}});
 await ctx.route('**/*',r=>{if(new URL(r.request().url()).hostname!=='127.0.0.1'){external.push(r.request().url());return r.abort();}return r.continue();});
 page=await ctx.newPage();page.setDefaultTimeout(15000);page.on('pageerror',e=>errors.push(e.message));page.on('dialog',d=>d.accept());
 await page.goto('http://127.0.0.1:3001/login');await page.getByLabel('Логин',{exact:true}).fill(name);await page.getByLabel('Пароль',{exact:true}).fill(password);await page.getByRole('button',{name:'Войти',exact:true}).click();await page.waitForURL('**/cabinet/**');
 await page.getByLabel('Переключить кабинет и подразделение').waitFor();
}
try{
 await login(fixture.staff_login);await page.getByRole('heading',{name:'Домашние задания',exact:true}).waitFor();
 assert.equal(await page.getByRole('button',{name:'+ Создать задание',exact:true}).count(),0);checks.push('staff view hides create');
 await page.getByLabel('Переключить кабинет и подразделение').selectOption('1001:1002');await page.getByRole('button',{name:'+ Создать задание',exact:true}).waitFor();checks.push('staff division switch enables permitted editing');
 await login('dev_supervisor');await page.getByRole('button',{name:'Диапазон дат',exact:true}).click();await page.locator('#rep-from').fill('2026-10-01');await page.locator('#rep-to').fill('2026-10-31');await page.getByRole('button',{name:'Создать',exact:true}).click();await page.getByLabel(/Колонки/).selectOption('full');
 await page.getByText(`DEV BFF ${demo.run} · Конституционное право`,{exact:true}).first().waitFor();await page.getByText('87',{exact:true}).first().waitFor();await page.getByText('93',{exact:true}).first().waitFor();checks.push('supervisor report displays local assignments and real grades');await page.screenshot({path:root+'dev/local/bff-supervisor.png',fullPage:true});
 await login('dev_examiner_1');await page.getByText('Раздел ещё не подключён',{exact:false}).waitFor();await page.getByLabel('Переключить кабинет и подразделение').selectOption('1001:1002');await page.getByText('Раздел ещё не подключён',{exact:false}).waitFor();checks.push('examiner context works; disconnected section explicit');
 await login('dev_admin');const type='DEV UI '+Date.now();
 await page.getByText('Типы домашних работ этого подразделения',{exact:true}).click();await page.getByLabel('Название типа').fill(type);await page.getByRole('button',{name:'Добавить тип',exact:true}).click();await page.getByLabel('Тип для изменения').selectOption(type);await page.getByLabel('Название типа').fill(type+' renamed');await page.getByRole('button',{name:'Переименовать',exact:true}).click();await page.getByLabel('Тип для изменения').selectOption(type+' renamed');await page.getByRole('button',{name:'Архивировать',exact:true}).click();await page.waitForFunction(t=>!Array.from(document.querySelectorAll('select[aria-label="Тип для изменения"] option')).some(o=>o.textContent===t),type+' renamed');checks.push('type create rename archive through UI');
 await page.getByText('Типы домашних работ этого подразделения',{exact:true}).click();await page.getByRole('button',{name:'+ Создать задание',exact:true}).click();await page.locator('input[name=name]').fill(type+' assignment');await page.locator('select[name=type]').selectOption('ОВ');await page.getByRole('button',{name:'Создать',exact:true}).click();await page.getByText(type+' assignment',{exact:true}).waitFor();checks.push('built UI assignment create with explicit type');
 await page.goto('http://127.0.0.1:3001/cabinet/admin/monitoring');await page.getByRole('heading',{name:'Состояние загрузок'}).waitFor();await page.getByText('Передача в обработку',{exact:true}).waitFor();assert.equal(await page.getByText('Недостаточно прав',{exact:false}).count(),0);checks.push('admin monitoring');
 await page.goto('http://127.0.0.1:3001/cabinet/admin/homework-archive');await page.getByText(`DEV BFF ${demo.run} · Проверенная работа`,{exact:false}).first().waitFor();checks.push('admin archive');
 await page.goto('http://127.0.0.1:3001/cabinet/admin/assignments');await page.getByLabel('Переключить кабинет и подразделение').waitFor();await page.getByText(`DEV BFF ${demo.run} · Конституционное право`,{exact:true}).first().waitFor();await page.screenshot({path:root+'dev/local/bff-header-final.png',fullPage:true});
 assert.deepEqual(errors,[]);assert.deepEqual(external,[]);checks.push('no browser errors or external traffic');await fs.writeFile(root+'dev/local/bff-browser-roles-report.json',JSON.stringify({status:'passed',checks},null,2));console.log(JSON.stringify({status:'passed',checks}));
}catch(e){console.log((await page.locator('body').innerText()).slice(-6500));await page.screenshot({path:root+'dev/local/bff-roles-failure.png',fullPage:true});throw e;}finally{await browser.close();}
