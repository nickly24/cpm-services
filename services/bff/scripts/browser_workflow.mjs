import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const root=new URL('../../../',import.meta.url).pathname;
const {password}=JSON.parse(await fs.readFile(root+'dev/local/accounts.json','utf8'));
const demo=JSON.parse(await fs.readFile(root+'dev/local/bff-demo.json','utf8'));
const browser=await chromium.launch({executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
const checks=[],errors=[],external=[];let activePage;
async function session(login,wrong=false){
 const ctx=await browser.newContext({viewport:{width:1440,height:1000}});
 await ctx.route('**/*',r=>{const u=new URL(r.request().url());if(u.hostname!=='127.0.0.1'){external.push(u.hostname);return r.abort();}return r.continue();});
 const page=await ctx.newPage();page.on('pageerror',e=>errors.push(e.message));activePage=page;
 await page.goto('http://127.0.0.1:3001/login');await page.getByLabel('Логин',{exact:true}).fill(login);
 if(wrong){await page.getByLabel('Пароль',{exact:true}).fill('intentionally-wrong');await page.getByRole('button',{name:'Войти',exact:true}).click();await page.getByText('Неверный логин или пароль',{exact:true}).waitFor();checks.push('wrong password');}
 await page.getByLabel('Пароль',{exact:true}).fill(password);await page.getByRole('button',{name:'Войти',exact:true}).click();await page.waitForURL('**/cabinet/**');checks.push('login '+login);return {ctx,page};
}
async function waitText(p,text){await p.getByText(text,{exact:false}).first().waitFor();}
try{
 const {ctx,page}=await session('dev_student_01',true);
 const cookie=(await ctx.cookies('http://127.0.0.1:3001/bff')) .find(c=>c.name==='cpm_bff');assert(cookie?.httpOnly);assert.equal(cookie.sameSite,'Strict');
 assert.equal(await page.evaluate(()=>localStorage.getItem('auth_token')),null);checks.push('HttpOnly cookie; no localStorage token');
 await page.reload();await page.getByRole('heading',{name:'Домашние задания',exact:true}).waitFor();checks.push('reload restores session');
 const tab=await ctx.newPage();await tab.goto('http://127.0.0.1:3001/cabinet/student/homework');await tab.getByRole('heading',{name:'Домашние задания',exact:true}).waitFor();
 await page.getByLabel('Переключить кабинет и подразделение').selectOption('1001:1002');
 await waitText(page,`DEV BFF ${demo.run} · Работа резервного кабинета`);await waitText(tab,`DEV BFF ${demo.run} · Работа резервного кабинета`);
 assert.equal(await page.getByText(`DEV BFF ${demo.run} · Конституционное право`,{exact:true}).count(),0);checks.push('division switch clears old work in both tabs');
 await page.getByLabel('Переключить кабинет и подразделение').selectOption('2001:2001');await waitText(page,'Здесь пока нет заданий');checks.push('organization switch');
 await page.getByLabel('Переключить кабинет и подразделение').selectOption('1001:1001');await waitText(page,`DEV BFF ${demo.run} · Проверенная работа`);await tab.close();
 await page.goto('http://127.0.0.1:3001/cabinet/student/homework?work='+demo.file_homework);await page.getByRole('dialog').waitFor();await waitText(page,'DEV: добавьте обоснование');
 await page.locator('input[type=file]').setInputFiles(root+'dev/local/bff-demo.pdf');
 await page.getByRole('button',{name:'Отправить исправления',exact:true}).waitFor({timeout:65000});
 await page.getByRole('button',{name:'Отправить исправления',exact:true}).click();await waitText(page,'Работа отправлена');checks.push('browser PDF upload, worker processing and resubmission');
 await page.screenshot({path:root+'dev/local/bff-student-workflow.png',fullPage:true});
 const reviewer=await session('dev_proctor_1');activePage=reviewer.page;
 await reviewer.page.goto('http://127.0.0.1:3001/cabinet/proctor/review-queue');
 const card=reviewer.page.locator('article').filter({hasText:`DEV BFF ${demo.run} · Проверенная работа`});await card.getByRole('button',{name:'Открыть',exact:true}).click();
 await reviewer.page.getByRole('button',{name:'Взять на проверку',exact:true}).click();await reviewer.page.getByLabel('Оценка',{exact:true}).fill('93');
 await reviewer.page.getByRole('button',{name:'Сохранить оценку · 93',exact:true}).click();await waitText(reviewer.page,'Работа проверена. Оценка сохранена.');checks.push('reviewer claim and grade in existing UI');
 await reviewer.page.screenshot({path:root+'dev/local/bff-reviewer.png',fullPage:true});
 await reviewer.page.goto('http://127.0.0.1:3001/cabinet/proctor/homework-archive');await waitText(reviewer.page,`DEV BFF ${demo.run} · Проверенная работа`);checks.push('graded PDF in archive');
 await page.reload();await waitText(page,'93');checks.push('student sees real grade');
 const admin=await session('dev_admin');activePage=admin.page;await waitText(admin.page,'Пользователи').catch(()=>{});
 await admin.page.getByRole('button',{name:'+ Создать задание',exact:true}).click();await admin.page.locator('input[name=name]').fill(`DEV BFF ${demo.run} · Создано через UI`);await admin.page.locator('select[name=type]').selectOption('ОВ');
 const adminTab=await admin.ctx.newPage();await adminTab.goto('http://127.0.0.1:3001/cabinet/admin/assignments');await adminTab.getByLabel('Переключить кабинет и подразделение').selectOption('1001:1002');await waitText(adminTab,'Закройте редактор');checks.push('cross-tab context switch blocked by editor');
 await admin.page.getByRole('button',{name:'Создать',exact:true}).click();await waitText(admin.page,`DEV BFF ${demo.run} · Создано через UI`);checks.push('assignment created through existing form');await adminTab.close();
 await admin.page.screenshot({path:root+'dev/local/bff-admin.png',fullPage:true});
 await page.goto('http://127.0.0.1:3001/cabinet/student/tests');await waitText(page,'Раздел ещё не подключён');checks.push('unconnected section honestly unavailable');
 await page.getByRole('button',{name:'Выйти из кабинета'}).click();await page.waitForURL('**/login');await page.reload();await page.getByRole('button',{name:'Войти',exact:true}).waitFor();checks.push('logout persists');
 assert.deepEqual(errors,[]);assert.deepEqual(external,[]);checks.push('no browser errors or external requests');
 await fs.writeFile(root+'dev/local/bff-browser-report.json',JSON.stringify({status:'passed',checks},null,2));console.log(JSON.stringify({status:'passed',checks}));
}catch(e){if(activePage){await activePage.screenshot({path:root+'dev/local/bff-browser-failure.png',fullPage:true});console.log((await activePage.locator('body').innerText()).slice(-7000));}throw e;}finally{await browser.close();}
