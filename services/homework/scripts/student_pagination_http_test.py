"""Isolated real BFF/Homework/MySQL student pagination; no shared fixtures or credentials."""
import json
import urllib.parse
import gateway_http_test as fixture

class PageSuite(fixture.Suite):
    def run(self):
        self.sql("INSERT INTO hw_types(id,organization_id,division_id,name) VALUES(1,1,2,'Московское право'),(2,1,2,'ОВ'),(3,1,3,'Foreign division'),(4,99,2,'Foreign org');")
        states=['none','uploading','processing','draft','submitted','in_review','revision_requested','graded']
        for i in range(32):
            n=i+1
            self.sql(f"INSERT INTO hw_assignments(id,organization_id,division_id,type_id,name,deadline,published) VALUES({n},1,2,{1 if i%2==0 else 2},'Задание {i:02d}','2026-10-{i%4+1:02d}',1);")
            self.sql(f"INSERT INTO hw_submissions(assignment_id,account_id,state,revision_comment) VALUES({n},7,'{states[i%8]}','Synthetic'),({n},8,'graded','');")
        self.sql("INSERT INTO hw_results(assignment_id,account_id,status,result) VALUES(1,7,1,0),(9,7,0,0);")
        for n,org,div,kind,pub,deleted in [(101,1,2,1,0,'NULL'),(102,1,2,1,1,'UTC_TIMESTAMP()'),(103,1,3,3,1,'NULL'),(104,99,2,4,1,'NULL')]:
            self.sql(f"INSERT INTO hw_assignments(id,organization_id,division_id,type_id,name,deadline,published,deleted_at) VALUES({n},{org},{div},{kind},'Hidden scope','2026-10-01',{pub},{deleted});")
        self.login('student')
        old=self.api('student','assignments?limit=100')['items']
        self.check({h['type_name'] for h in old}=={'Московское право','ОВ'},'fixture and API preserve Cyrillic names')
        def matches(h,status):
            w=h['workspace'];state=w['submission']['state'];r=w['legacy_result']
            done=state=='graded' or state=='none' and r is not None and r['status']==1 and r['result'] is not None
            return {'all':True,'done':done,'in_review':state in ['submitted','in_review'],'revision':state=='revision_requested','undone':state not in ['submitted','in_review','graded'] and not done}[status]
        expected={s:sum(matches(h,s) for h in old) for s in ['all','undone','in_review','revision','done']}
        self.check(expected=={'all':32,'undone':19,'in_review':8,'revision':4,'done':5},'status oracle: revision overlaps undone, manual zero/clear preserved')
        ordered=sorted(old,key=lambda h:(h['deadline'],h['id']))
        self.sql("SET GLOBAL log_output='TABLE'; SET GLOBAL general_log=ON;")
        first=self.api('student','assignments?page=1&limit=12')
        self.sql('SET GLOBAL general_log=OFF;')
        queries=self.sql("SELECT argument FROM mysql.general_log WHERE command_type='Execute';").splitlines()
        business=[q for q in queries if 'FROM hw_assignments h JOIN hw_types t' in q]
        self.check(len(business)==2,'two SQL reads only: aggregate counts + bounded page; no workspace N+1')
        self.check(len(first['items'])==12 and first['counts']==expected,'12 cards with 32 counted')
        self.check(first['pagination']=={'current_page':1,'total_pages':3,'total_items':32,'items_per_page':12},'pagination metadata')
        self.check([h['homework_id'] for h in first['items']]==[h['id'] for h in ordered[:12]],'deadline/id sorting before LIMIT')
        self.check(all('workspace' not in h and 'active_job' not in h and 'limits' not in h for h in first['items']),'lightweight cards only')
        rest=[self.api('student',f'assignments?page={p}&limit=12') for p in [2,3]]
        ids=[h['homework_id'] for p in [first,*rest] for h in p['items']]
        self.check(ids==[h['id'] for h in ordered] and len(set(ids))==32,'pages cover visible assignments without duplicate/missing IDs')
        for status in expected:
            selected=[h for h in ordered if matches(h,status)]
            for page in range(1,max(1,(len(selected)+2)//3)+1):
                response=self.api('student',f'assignments?page={page}&limit=3&status={status}')
                self.check(response['counts']==expected,'tab counts independent of selected page/status')
                self.check([h['homework_id'] for h in response['items']]==[h['id'] for h in selected[(page-1)*3:page*3]],'status filter before pagination')
                self.check(response['pagination']['total_items']==len(selected),'status total is count, not page length')
        for kind in ['Московское право','ОВ']:
            response=self.api('student','assignments?'+urllib.parse.urlencode(dict(page=1,limit=2,type=kind,search='Задание 0')))
            selected=[h for h in ordered if h['type_name']==kind and 'Задание 0' in h['name']]
            self.check(len(selected)==5,'combined type/search fixture has positive matches')
            self.check(response['counts']=={s:sum(matches(h,s) for h in selected) for s in expected},'type+search counters have same exact scope')
            self.check([h['homework_id'] for h in response['items']]==[h['id'] for h in selected[:2]],'type+search page')
        for search in ['Московское право','%','_','not present']:
            response=self.api('student','assignments?'+urllib.parse.urlencode(dict(page=1,search=search)))
            selected=[h for h in old if search in h['name']+' '+h['type_name']]
            self.check(response['counts']['all']==len(selected),'literal substring search, including wildcard characters')
        empty=self.api('student','assignments?page=99&search=nonexistent')
        self.check(empty['items']==[] and empty['pagination']['current_page']==1 and empty['pagination']['total_pages']==1,'empty page normalized')
        last=self.api('student','assignments?page=999&limit=12')
        self.check(last['pagination']['current_page']==3 and len(last['items'])==8,'out-of-range page clamps')
        other=self.api('other','assignments?page=1&limit=12')
        self.check(other['counts']['done']==32 and first['counts']['done']==5,'account ID isolates results')
        for query in ['page=0','page=-1','page=abc','page=1000001','page=1&limit=0','page=1&limit=101','page=1&status=invalid','page=1&status=all&status=done','page=1&after=1','page=1&account_id=8','page=1&type_id=1']:
            self.api('student','assignments?'+query,code=400)
        self.api('admin','assignments?page=1',code=403)
        fixture.USERS['student']['division_id']=3
        cabinet=self.api('student','assignments?page=1')
        self.check(cabinet['counts']['all']==1 and cabinet['items'][0]['homework_id']==103,'new cabinet has own counts/cards')
        fixture.USERS['student']['division_id']=2
        self.sql('UPDATE hw_assignments SET published=0 WHERE id=32;')
        changed=self.api('student','assignments?page=1&limit=12')
        self.check(changed['counts']['all']==31 and changed['counts']['done']==4,'next request sees publication changes')
        self.sql("UPDATE hw_submissions SET state='submitted' WHERE assignment_id=4 AND account_id=7;")
        changed=self.api('student','assignments?page=1&status=in_review')
        self.check(changed['counts']['in_review']==9 and changed['counts']['undone']==18,'submission transitions update counts/items')
        fixture.BLOCKED.add('student');self.api('student','assignments?page=1',code=401)
        print(json.dumps(dict(pagination_sql_reads=len(business),visible_assignments=32,first_page_items=12,status_counts=expected,isolated_real_http=True)))

if __name__=='__main__':
    fixture.Suite=PageSuite
    fixture.main()
