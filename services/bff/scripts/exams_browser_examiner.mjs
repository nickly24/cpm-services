// Real two-examiner workflow. Only the demo's second student's synthetic history
// is reset through its public confirmation API to make this script repeatable.
import { chromium } from '/Users/nikolajpribys/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright/index.mjs';
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
const root = new URL('../../../', import.meta.url).pathname;
const demo = JSON.parse(await fs.readFile(root+'dev/local/bff-exams-demo.json','utf8'));
const {password} = JSON.parse(await fs.readFile(root+'dev/local/accounts.json','utf8'));
const browser = await chromium.launch({executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
const checks=[], errors=[], external=[], keys=[];
async function login(login,role) {
 const ctx=await browser.newContext({viewport:{width:1440,height:1000}});
 await ctx.route('**/*',route=>{const url=new URL(route.request().url()); if(url.hostname!=='127.0.0.1'){external.push(url.hostname);return route.abort();}return route.continue();});
 const page=await ctx.newPage();page.setDefaultTimeout(25000);page.on('pageerror',error=>errors.push(error.message));page.on('dialog',dialog=>dialog.accept());
 await page.goto('http://127.0.0.1:3001/login');await page.getByLabel('Логин',{exact:true}).fill(login);await page.getByLabel('Пароль',{exact:true}).fill(password);await page.getByRole('button',{name:'Войти',exact:true}).click();await page.waitForURL('**/cabinet/**');
 await page.getByLabel('Переключить кабинет и подразделение').selectOption(`${demo.organization_id}:${demo.division_a}`);
 await page.waitForFunction(value=>document.querySelector('[aria-label="Переключить кабинет и подразделение"]')?.value===value,`${demo.organization_id}:${demo.division_a}`);
 await page.goto(`http://127.0.0.1:3001/cabinet/${role}/exams`);
 return {ctx,page};
}
async function api(page,path,method='GET',body,confirmation) {
 return page.evaluate(async({path,method,body,confirmation})=>{
  const identity=await (await fetch('/bff/auth/me',{headers:{'X-CPM-Request':'1'}})).json();
  const response=await fetch('/bff'+path,{method,headers:{'X-CPM-Request':'1','X-CPM-Context':identity.context_revision,...(method==='GET'?{}:{'Content-Type':'application/json','Idempotency-Key':crypto.randomUUID()}),...(confirmation?{'X-Exam-Confirmation':confirmation}:{})},body:body===undefined?undefined:JSON.stringify(body)});
  const result=await response.json(); if(!response.ok)throw new Error(`${response.status} ${result.error}`);return result.data;
 },{path,method,body,confirmation});
}
async function refresh(page) {const response=page.waitForResponse(r=>/\/bff\/exams\/\d+\/attempts\/\d+$/.test(new URL(r.url()).pathname)&&r.request().method()==='GET'&&r.status()===200);await page.getByRole('button',{name:'Обновить состояние',exact:true}).click();await response;await page.waitForFunction(()=>[...document.querySelectorAll('button')].find(b=>b.textContent==='Обновить состояние')?.disabled===false);}
async function vote(page,value) {
 const name=value===0?'0 · Неудовлетворительный':value===0.5?'0,5 · Частичный':'1 · Полный';
 await page.getByRole('button',{name,exact:true}).click();await page.getByRole('dialog').getByRole('button',{name:'Подтвердить',exact:true}).click();await page.getByRole('dialog').waitFor({state:'hidden'});
}
try {
 const admin=await login('dev_admin','admin'), examId=demo.classic_exam_id;
 assert(demo.student2_login.startsWith('dev_bff_exams_'));
 const history=await api(admin.page,`/exams/${examId}/history/${demo.student2_account_id}/delete-preview`);
 if(history.counts.attempts>0)await api(admin.page,`/exams/${examId}/history/${demo.student2_account_id}`,'DELETE',{expected_history_generation:history.history_generation},history.confirmation);
 const privileges=await api(admin.page,`/exams/${examId}/privileges`), old=privileges.items.find(p=>p.student_account_id===demo.student2_account_id);
 await api(admin.page,`/exams/${examId}/privileges/${demo.student2_account_id}`,'PUT',{expected_revision:old?.revision??0,replacement_limit:1});await admin.ctx.close();
 const first=await login(demo.examiners[0].login,'examinator'), second=await login(demo.examiners[1].login,'examinator');
 await first.page.goto(`http://127.0.0.1:3001/cabinet/examinator/exams?examId=${examId}`);
 const card=first.page.locator('article').filter({hasText:`ID ${demo.student2_account_id}`});await card.getByRole('button',{name:'Открыть сдачу',exact:true}).click();await first.page.getByRole('heading',{name:'Подготовка к экзамену',exact:true}).waitFor();
 const attemptId=Number(new URL(first.page.url()).searchParams.get('attemptId'));assert(attemptId>0);
 checks.push('assigned examiner creates/reopens the correct student attempt');
 await second.page.goto(first.page.url());await second.page.getByRole('heading',{name:'Подготовка к экзамену',exact:true}).waitFor();
 assert.equal(await first.page.getByRole('button',{name:'Начать экзамен',exact:true}).isEnabled(),false);
 await first.page.getByLabel('Переключить кабинет и подразделение').selectOption(`${demo.organization_id}:${demo.division_b}`);
 await first.page.getByRole('alert').filter({hasText:'Закройте редактор'}).waitFor();assert.equal(await first.page.getByLabel('Переключить кабинет и подразделение').inputValue(),`${demo.organization_id}:${demo.division_a}`);
 checks.push('live attempt blocks cabinet switching, including before start');
 await Promise.all([first.page.waitForResponse(r=>r.url().endsWith('/ready')&&r.status()===200),first.page.getByRole('button',{name:'Я готов принимать экзамен',exact:true}).click()]);await first.page.waitForFunction(()=>[...document.querySelectorAll('button')].find(b=>b.textContent==='Я готов принимать экзамен')?.disabled);
 await Promise.all([second.page.waitForResponse(r=>r.url().endsWith('/ready')&&r.status()===200),second.page.getByRole('button',{name:'Я готов принимать экзамен',exact:true}).click()]);await second.page.waitForFunction(()=>[...document.querySelectorAll('button')].find(b=>b.textContent==='Я готов принимать экзамен')?.disabled);
 await refresh(first.page);await first.page.getByRole('button',{name:'Начать экзамен',exact:true}).waitFor();await first.page.waitForFunction(()=>[...document.querySelectorAll('button')].find(b=>b.textContent==='Начать экзамен')?.disabled===false);
 checks.push('start requires readiness from both independent committee sessions');
 let lost=false;await first.page.route(`**/bff/exams/${examId}/attempts/${attemptId}/start`,async route=>{keys.push(route.request().headers()['idempotency-key']);if(!lost){lost=true;const response=await route.fetch();assert.equal(response.status(),200);return route.abort('failed');}return route.continue();});
 await first.page.getByRole('button',{name:'Начать экзамен',exact:true}).click();await first.page.getByRole('button',{name:'Повторить тот же запрос',exact:true}).waitFor();assert.equal(keys.length,1);
 await first.page.getByRole('button',{name:'Повторить тот же запрос',exact:true}).click();await first.page.getByRole('heading',{name:/Вопрос 1/}).waitFor();assert.equal(keys.length,2);assert.equal(keys[0],keys[1]);
 checks.push('lost successful start retains UUID and explicit replay returns one attempt');
 await first.page.getByRole('button',{name:'Заменить вопрос',exact:true}).click();await first.page.getByRole('dialog').getByRole('button',{name:'Подтвердить',exact:true}).click();await first.page.getByRole('dialog').waitFor({state:'hidden'});await first.page.getByRole('heading',{name:/Вопрос 2/}).waitFor();
 const state=await api(first.page,`/exams/${examId}/attempts/${attemptId}`);assert.equal(state.progress.replacement_used,1);const current=state.current_question.id;
 checks.push('replacement consumes one allowance and creates a new presented question');
 await refresh(second.page);await vote(first.page,0);
 const colleague=await api(second.page,`/exams/${examId}/attempts/${attemptId}`);assert.equal(colleague.current_question.round.votes_count,1);assert.equal(colleague.current_question.round.votes,undefined);assert.equal(colleague.current_question.round.own_vote,undefined);
 checks.push('open round hides the other examiner vote');
 await vote(second.page,1);await refresh(first.page);await first.page.getByText('Оценки комиссии разошлись.',{exact:false}).waitFor();await first.page.getByText(/Раунд 2 · Получено 0 из 2/).waitFor();
 checks.push('disagreement displays closed votes and opens a fresh round');
 await vote(first.page,1);await vote(second.page,1);await refresh(first.page);await first.page.getByText('Согласованная оценка: 1',{exact:true}).waitFor();
 assert.equal(await first.page.getByRole('button',{name:'Заменить вопрос',exact:true}).isEnabled(),false);
 checks.push('unanimity closes a question; voted questions cannot be replaced');
 for(let i=1;i<5;i++) {
  await first.page.getByRole('button',{name:'Следующий вопрос',exact:true}).click();await first.page.waitForFunction(()=>[...document.querySelectorAll('button')].some(b=>b.textContent==='1 · Полный'&&!b.disabled));await refresh(second.page);await vote(first.page,1);await vote(second.page,1);await refresh(first.page);
 }
 await first.page.getByRole('heading',{name:'Экзамен завершён',exact:true}).waitFor();await first.page.getByText('5 / 5',{exact:true}).first().waitFor();assert.equal(await first.page.getByRole('button',{name:'Следующий вопрос',exact:true}).count(),0);
 checks.push('five regular consensuses publish the real result automatically');
 await first.page.getByRole('button',{name:'Открыть историю всех вопросов',exact:true}).click();await first.page.getByRole('heading',{name:'Протокол вопросов',exact:true}).waitFor();await first.page.getByText(`Выдан вместо вопроса #${state.current_question.replaces_id}`,{exact:true}).waitFor();
 await first.page.getByRole('button',{name:'История голосования',exact:true}).nth(1).click();await first.page.getByText('Обновить историю раундов',{exact:true}).waitFor();
 checks.push('completed protocol retains replaced question and disputed/consensus rounds');
 await first.page.setViewportSize({width:390,height:844});await first.page.waitForTimeout(300);assert(await first.page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth));await first.page.screenshot({path:root+'dev/local/bff-exams-examiner-mobile.png',fullPage:true});
 checks.push('oral exam and protocol fit a 390px viewport');
 await first.page.getByLabel('Переключить кабинет и подразделение').selectOption(`${demo.organization_id}:${demo.division_b}`);await first.page.waitForURL(url=>!url.searchParams.has('attemptId'));await first.page.getByRole('heading',{name:'Мои экзамены',exact:true}).waitFor();
 checks.push('completed attempt releases context guard and switching clears personal URL');
 assert.deepEqual(errors,[]);assert.deepEqual(external,[]);checks.push('no JavaScript errors or remote requests');
 await fs.writeFile(root+'dev/local/bff-exams-examiner-report.json',JSON.stringify({status:'passed',checks,examId,attemptId,replacedPresentedQuestionId:current},null,2));console.log(JSON.stringify({status:'passed',checks:checks.length,examId,attemptId}));
} catch(error) {for(const ctx of browser.contexts())for(const page of ctx.pages())await fs.writeFile(root+'dev/local/bff-exams-examiner-failure-'+browser.contexts().indexOf(ctx)+'.txt',await page.locator('body').innerText()).catch(()=>{});await fs.writeFile(root+'dev/local/bff-exams-examiner-report.json',JSON.stringify({status:'failed',checks,error:String(error),errors,external},null,2));throw error;} finally {await browser.close();}
