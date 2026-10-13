// Read-only checks against the optimized local UI after the full dev workflows.
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const root=new URL('../../../',import.meta.url).pathname;
const {password}=JSON.parse(await fs.readFile(root+'dev/local/accounts.json','utf8'));
const demo=JSON.parse(await fs.readFile(root+'dev/local/bff-tests-demo.json','utf8'));
const browser=await chromium.launch({executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
const externalDisabled=process.argv.includes('--external-disabled');
const checks=[],errors=[],external=[],legacy=[],domainWrites=[],externalRequests=[],studentRequests=[];
async function login(name){
 const ctx=await browser.newContext({viewport:{width:1440,height:1000}});
 await ctx.route('**/*',r=>{const u=new URL(r.request().url());if(u.hostname!=='127.0.0.1'){external.push(u.hostname);return r.abort();}return r.continue();});
 const page=await ctx.newPage();page.setDefaultTimeout(25000);page.on('pageerror',e=>errors.push(e.message));page.on('request',r=>{const u=new URL(r.url());if(u.pathname.startsWith('/bff/tests/external-'))externalRequests.push(u.pathname);if(name===demo.browser_student_login&&u.pathname.startsWith('/bff/tests/'))studentRequests.push(u.pathname);if(u.pathname.startsWith('/api/')||u.pathname.startsWith('/bff/unconnected'))legacy.push(u.pathname);if(!['GET','HEAD'].includes(r.method())&&!u.pathname.startsWith('/bff/auth/'))domainWrites.push(r.method()+' '+u.pathname);});
 await page.goto('http://127.0.0.1:3001/login');await page.getByLabel('Логин',{exact:true}).fill(name);await page.getByLabel('Пароль',{exact:true}).fill(password);await page.getByRole('button',{name:'Войти',exact:true}).click();await page.waitForURL('**/cabinet/**');
 const cookie=(await ctx.cookies()).find(c=>c.name==='cpm_bff');assert(cookie?.httpOnly);assert.equal(cookie.sameSite,'Strict');
 return {ctx,page};
}
try{
 let {ctx,page}=await login('dev_admin');await page.goto('http://127.0.0.1:3001/cabinet/admin/tests');await page.getByRole('button',{name:'Банк организации',exact:true}).waitFor();await page.locator('article').first().waitFor();checks.push('optimized full-admin catalog and bank load');
 const pendingExternal=page.getByRole('status').getByText('Тесты вне системы пока недоступны.',{exact:false});
 if(externalDisabled) await pendingExternal.waitFor();
 else assert.equal(await pendingExternal.count(),0);
 await page.getByRole('button',{name:'Создать тест',exact:true}).click();
 assert.equal(await page.getByRole('menuitem',{name:'Вне системы',exact:true}).isDisabled(),externalDisabled);
 await page.keyboard.press('Escape');checks.push(externalDisabled?'explicit external feature flag disables creation':'Dev external tests creation is enabled');
 await page.setViewportSize({width:390,height:844});await page.waitForTimeout(300);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));await page.screenshot({path:root+'dev/local/bff-tests-optimized-mobile.png',fullPage:true});checks.push('390px catalog has no page overflow');await ctx.close();
 ({ctx,page}=await login(demo.results_login));await page.waitForURL('**/cabinet/*/test-results');await page.getByRole('heading',{name:'Результаты тестов',exact:true}).waitFor();assert.equal(await page.getByRole('link',{name:'Тесты',exact:true}).count(),0);checks.push('results-only staff lands in authorized results');await ctx.close();
 ({ctx,page}=await login(demo.browser_student_login));await page.goto('http://127.0.0.1:3001/cabinet/student/tests');const title=`DEV BFF Tests ${demo.run} · Для прохождения в UI`;await page.getByRole('button').filter({has:page.locator('span').filter({hasText:title})}).first().click();await page.getByText('100',{exact:true}).waitFor();checks.push('optimized student UI shows real ready result');await page.screenshot({path:root+'dev/local/bff-tests-optimized-student.png',fullPage:true});await ctx.close();
 assert(studentRequests.some(path=>path==='/bff/tests/catalog-with-access'));
 assert.equal(studentRequests.filter(path=>/^\/bff\/tests\/[^/]+\/access$/.test(path)).length,0);
 checks.push('student catalog uses bulk access with no per-test access requests');
 assert.deepEqual(errors,[]);assert.deepEqual(external,[]);assert.deepEqual(legacy,[]);assert.deepEqual(domainWrites,[]);if(externalDisabled)assert.deepEqual(externalRequests,[]);checks.push('no JavaScript errors, remote/legacy traffic or business writes; cookie is HttpOnly');
 await fs.writeFile(root+'dev/local/bff-tests-optimized-report.json',JSON.stringify({status:'passed',checks,errors,external,legacy,domainWrites,externalDisabled,externalRequests,studentRequests},null,2));console.log(JSON.stringify({status:'passed',checks}));
}finally{await browser.close();}
