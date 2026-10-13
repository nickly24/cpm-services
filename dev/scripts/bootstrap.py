"""Import fresh structure and seed fictitious data, only into fixed loopback dev DBs."""
import datetime as dt
import hashlib
import json
import re
import secrets
from pathlib import Path
from urllib.parse import quote
from zoneinfo import ZoneInfo
import mysql.connector
from pymongo import MongoClient
from bson import ObjectId, json_util
from werkzeug.security import generate_password_hash
from export_structure import read_env

BASE=Path(__file__).resolve().parents[1]

def settings():
    v=read_env(BASE/'.env')
    if v['MYSQL_DATABASE']!='cpm_dev' or v['MONGO_DATABASE']!='cpm_dev':
        raise RuntimeError('Only cpm_dev targets are supported')
    if int(v['MYSQL_PORT'])!=13306 or int(v['MONGO_PORT'])!=27018:
        raise RuntimeError('Only dedicated loopback dev ports are supported')
    return v

def mysql_connection(v):
    return mysql.connector.connect(host='127.0.0.1',port=13306,user=v['MYSQL_USER'],password=v['MYSQL_PASSWORD'],database='cpm_dev',connection_timeout=10)

def mongo_client(v,root=False):
    return MongoClient(host='127.0.0.1',port=27018,username=v['MONGO_ROOT_USER'] if root else v['MONGO_USER'],password=v['MONGO_ROOT_PASSWORD'] if root else v['MONGO_PASSWORD'],authSource='admin' if root else 'cpm_dev',serverSelectionTimeoutMS=10000)

def insert(cur,table,values):
    names=list(values)
    cur.execute('INSERT INTO `'+table+'` ('+','.join('`'+n+'`' for n in names)+') VALUES ('+','.join(['%s']*len(names))+')',tuple(values.values()))

def schema(v):
    meta=json.loads((BASE/'schema/mysql.json').read_text())
    conn=mysql_connection(v);cur=conn.cursor()
    try:
        cur.execute('SHOW TABLES'); existing={r[0] for r in cur.fetchall()}
        extensions=set(json.loads((BASE/'schema/extensions.json').read_text()))
        if existing-set(meta['ddl'])-extensions:raise RuntimeError('Unexpected tables in dev database; no destructive changes made')
        cur.execute('SET FOREIGN_KEY_CHECKS=0')
        try:
            for name,ddl in meta['ddl'].items():
                if name not in existing:
                    if not ddl.startswith('CREATE TABLE `'+name+'`'):raise RuntimeError('Invalid table DDL')
                    cur.execute(ddl)
        finally:cur.execute('SET FOREIGN_KEY_CHECKS=1')
        conn.commit()
    finally:cur.close();conn.close()
    meta=json_util.loads((BASE/'schema/mongo.json').read_text())
    client=mongo_client(v,True);db=client.cpm_dev
    try:
        if not db.command('usersInfo',v['MONGO_USER'])['users']:
            db.command('createUser',v['MONGO_USER'],pwd=v['MONGO_PASSWORD'],roles=[{'role':'readWrite','db':'cpm_dev'}])
        existing=set(db.list_collection_names())
        for coll in meta['collections']:
            if coll['name'] not in existing:db.create_collection(coll['name'],**coll['options'])
            for idx in coll['indexes']:
                if idx['name']=='_id_':continue
                opts={k:v for k,v in idx.items() if k not in {'key','v','ns','background'}}
                db[coll['name']].create_index(list(idx['key'].items()),**opts)
    finally:client.close()

def seed(v):
    marker=BASE/'local/seed-complete.json'
    if marker.exists():
        print('Synthetic seed already completed; retained existing dev data.')
        return
    now=dt.datetime.now(dt.timezone.utc);today=now.date()
    sql_marker=BASE/'local/seed-mysql-complete.json'
    if sql_marker.exists():
        accounts=json.loads((BASE/'local/accounts.json').read_text())['accounts']
    else:
        conn=mysql_connection(v);cur=conn.cursor()
        try:
            cur.execute('SELECT COUNT(*) FROM students');n=cur.fetchone()[0]
            if n:raise RuntimeError('Existing students detected; seed refuses to overwrite data')
            credentials=BASE/'local/accounts.json'
            if credentials.exists():password=json.loads(credentials.read_text())['password']
            else:
                password=secrets.token_urlsafe(18)
                credentials.write_text(json.dumps({'password':password,'note':'Synthetic local accounts only'},indent=2)+'\n');credentials.chmod(0o600)
            hashed=generate_password_hash(password,method='pbkdf2:sha256:600000')
            accounts=[]
            def account(role,entity_id,login):
                insert(cur,'auth_users',{'username':login,'password':hashed,'role':role,'ref_id':entity_id});accounts.append({'login':login,'role':role,'id':entity_id})
            for i,name in [(1001,'DEV Математика'),(1002,'DEV Информатика')]:insert(cur,'directions',{'id':i,'name':name})
            for i in (1,2):
                insert(cur,'schools',{'id':1000+i,'name':f'DEV Школа {i}','short_name':f'DEV-{i}','notes':'Вымышленная школа для разработки','is_active':1})
                insert(cur,'groups',{'id':1000+i,'name':f'DEV Группа {i}'})
                insert(cur,'proctors',{'id':1000+i,'full_name':f'DEV Проктор {i}','group_id':1000+i});account('proctor',1000+i,f'dev_proctor_{i}')
            insert(cur,'admins',{'id':1001,'full_name':'DEV Администратор'});account('admin',1001,'dev_admin')
            insert(cur,'supervisors',{'id':1001,'full_name':'DEV Супервайзер'});account('supervisor',1001,'dev_supervisor')
            for i in range(1,4):
                insert(cur,'examinators',{'id':1000+i,'full_name':f'DEV Экзаменатор {i}'});account('examinator',1000+i,f'dev_examiner_{i}')
            insert(cur,'admin_roles',{'id':1001,'name':'DEV Учебный сотрудник','description':'Тестовая роль'})
            insert(cur,'admin_role_permissions',{'role_id':1001,'section':'users','can_view':1,'can_edit':1})
            insert(cur,'admin_role_users',{'id':1001,'full_name':'DEV Сотрудник','role_id':1001});account('staff_admin',1001,'dev_staff')
            for i in range(1,13):
                sid=1000+i;group=1001 if i<=6 else 1002
                insert(cur,'students',{'id':sid,'full_name':f'DEV Ученик {i:02d}','class':10 if i<=6 else 11,'group_id':group,'school_id':group,'tg_name':''})
                account('student',sid,f'dev_student_{i:02d}')
                insert(cur,'student_credentials',{'student_id':sid,'login':f'dev_student_{i:02d}','password':password})
            types=[('in_person','Очное присутствие'),('absent_valid','Отсутствие по уважительной причине'),('remote_permanent','Дистанционно постоянно'),('remote_scheduled','Дистанционно по расписанию'),('remote_valid','Дистанционно по уважительной причине'),('late','Опоздание'),('joined_later','Присоединился позднее'),('hybrid','Гибридное присутствие')]
            for i,(code,name) in enumerate(types,1):insert(cur,'attendance_types',{'id':i,'code':code,'name_ru':name,'sort_order':i})
            insert(cur,'class_days',{'id':1001,'date':today,'comment':'DEV учебный день'})
            for sid in range(1001,1007):insert(cur,'class_day_attendance',{'class_day_id':1001,'student_id':sid,'attendance_type_id':1})
            insert(cur,'zaps',{'id':1001,'student_id':1007,'text':'DEV Тестовый отгул','status':'set','answer':''})
            insert(cur,'zap_dates',{'zap_id':1001,'date':today,'status':'pending'})
            for i,typ in [(1,'ОВ'),(2,'ДЗНВ')]:insert(cur,'homework',{'id':1000+i,'name':f'DEV Домашняя работа {i}','type':typ,'deadline':today+dt.timedelta(days=7),'published':1})
            insert(cur,'homework_sessions',{'homework_id':1001,'student_id':1001,'status':1,'result':85,'date_pass':today})
            insert(cur,'homework_submissions',{'homework_id':1002,'student_id':1002,'state':'none'})
            insert(cur,'tests_out',{'id':1001,'direction_id':1001,'name':'DEV Внешний тест','date':today})
            insert(cur,'test_sessions',{'student_id':1001,'test_id':1001,'rate':80})
            insert(cur,'exams',{'id':1001,'name':'DEV Внешний экзамен','date':today,'exam_type':'outside_lms','direction_id':1001})
            insert(cur,'exam_sessions',{'exam_id':1001,'student_id':1001,'val':80,'points':4,'examinator':'DEV Внешний проверяющий'})
            insert(cur,'exams',{'id':1002,'name':'DEV Классический экзамен','date':today,'exam_type':'classic','direction_id':1001,'training_enabled':1})
            insert(cur,'classic_exam_settings',{'exam_id':1002,'start_at':now.replace(tzinfo=None)-dt.timedelta(days=1),'end_at':now.replace(tzinfo=None)+dt.timedelta(days=365),'fractional_mode':'round_up','tie_breaker_source_mode':'any_part','tie_breaker_half_mode':'round_up'})
            insert(cur,'classic_exam_parts',{'id':1001,'exam_id':1002,'code':'A','question_weight':3,'question_count':2,'sort_order':1})
            for i in range(1,4):
                q=f'DEV Сколько будет {i}+{i}?';a=str(i*2)
                insert(cur,'classic_exam_questions',{'id':1000+i,'exam_id':1002,'part_id':1001,'question_text':q,'answer_text':a,'content_hash':hashlib.sha256((q+'\n'+a).encode()).hexdigest(),'sort_order':i})
            for grade,score in [(0,0),(1,1),(2,2),(3,3),(4,4),(5,5)]:insert(cur,'classic_exam_grade_thresholds',{'exam_id':1002,'grade':grade,'min_score':score})
            insert(cur,'classic_exam_commissions',{'id':1001,'exam_id':1002,'name':'DEV Комиссия'})
            for i in range(1,4):insert(cur,'classic_exam_commission_members',{'commission_id':1001,'examinator_id':1000+i,'position':i})
            insert(cur,'classic_exam_assignments',{'id':1001,'exam_id':1002,'student_id':1001,'attempt_no':1,'source_commission_id':1001,'created_by_role':'admin','created_by':1001,'commission_name_snapshot':'DEV Комиссия'})
            for i in range(1,4):insert(cur,'classic_exam_assignment_members',{'assignment_id':1001,'examinator_id':1000+i,'position':i,'full_name_snapshot':f'DEV Экзаменатор {i}'})
            insert(cur,'card_themes',{'id':1001,'name':'DEV Основы','direction_id':1001})
            insert(cur,'cards',{'id':1001,'theme_id':1001,'question':'DEV Сколько будет 2+2?','answer':'4','sort_order':1})
            insert(cur,'rating_source_state',{'id':1})
            # No production tokens, bot settings, S3 keys, push destinations or PDF objects.
            conn.commit()
            credentials.write_text(json.dumps({'password':password,'accounts':accounts,'note':'Synthetic local accounts only'},ensure_ascii=False,indent=2)+'\n');credentials.chmod(0o600)
        except Exception:
            conn.rollback();raise
        finally:cur.close();conn.close()
        sql_marker.write_text(json.dumps({'completed_at':now.isoformat()})+'\n')
    client=mongo_client(v);db=client.cpm_dev
    try:
        tid=ObjectId('660000000000000000000001');vid=ObjectId('660000000000000000000002')
        questions=[{'questionId':1,'type':'single','text':'DEV Сколько будет 2+2?','points':1,'answers':[{'id':'a','text':'4','isCorrect':True},{'id':'b','text':'5','isCorrect':False}],'correctAnswers':[]},{'questionId':2,'type':'text','text':'DEV Напишите число три цифрой','points':1,'answers':[],'correctAnswers':['3']}]
        definition={'title':'DEV Тест для разработки','direction':'DEV Математика','questions':questions}
        digest=hashlib.sha256(json.dumps(definition,ensure_ascii=False,sort_keys=True,separators=(',',':')).encode()).hexdigest()
        db.tests.update_one({'_id':tid},{'$setOnInsert':{**definition,'published':True,'visible':True,'timeLimitMinutes':30,'startDate':(now-dt.timedelta(days=1)).isoformat(),'endDate':(now+dt.timedelta(days=365)).isoformat(),'createdAt':now.isoformat(),'currentVersionId':str(vid)}},upsert=True)
        db.test_versions.update_one({'_id':vid},{'$setOnInsert':{'testId':str(tid),**definition,'definitionHash':digest,'createdAtMoscow':now.astimezone(ZoneInfo('Europe/Moscow')).isoformat()}},upsert=True)
        db.schedule.update_one({'_id':ObjectId('660000000000000000000003')},{'$setOnInsert':{'date':today.isoformat(),'start_time':'10:00','end_time':'11:00','lesson_name':'DEV Занятие','teacher_name':'DEV Преподаватель','location':'DEV','classroom':'101','color':'#5B8DEF','is_public':True,'school_id':None,'is_in_person':True,'is_for_all':True,'is_changed':False,'created_at':now,'updated_at':now}},upsert=True)
    finally:client.close()
    marker.write_text(json.dumps({'completed_at':now.isoformat(),'students':12,'accounts':len(accounts),'origin':'synthetic'},indent=2)+'\n')
    print('Seed complete: 12 fictitious students, profiles, directories, homework, tests, exams, attendance and cards.')

def environments(v):
    (BASE/'local').mkdir(mode=0o700, parents=True, exist_ok=True)
    common={'MYSQL_HOST':'127.0.0.1','MYSQL_PORT':'13306','MYSQL_DATABASE':'cpm_dev','MYSQL_USER':v['MYSQL_USER'],'MYSQL_PASSWORD':v['MYSQL_PASSWORD'],'MYSQL_POOL_SIZE':'5','JWT_SECRET_KEY':v['JWT_SECRET_KEY']}
    uri='mongodb://'+quote(v['MONGO_USER'],safe='')+':'+quote(v['MONGO_PASSWORD'],safe='')+'@127.0.0.1:27018/cpm_dev?authSource=cpm_dev'
    files={'backend.env':{**common,'FLASK_ENV':'development','SECRET_KEY':v['JWT_SECRET_KEY'],'MONGODB_URI':uri,'MONGODB_DB_NAME':'cpm_dev'},'homework.env':{**common,'ENV':'development','PORT':'8001','RUN_HOMEWORK_WORKER':'false','S3_ENDPOINT_URL':'http://127.0.0.1:19000','S3_REGION':'us-east-1','S3_BUCKET':'cpm-dev-disabled','S3_ACCESS_KEY_ID':'dev-disabled','S3_SECRET_ACCESS_KEY':'dev-disabled'},'frontend.env':{'NEXT_PUBLIC_API_BASE_URL':'http://127.0.0.1:8000','NEXT_PUBLIC_HOMEWORK_SERVICE_URL':'http://127.0.0.1:8001'}}
    for name,values in files.items():
        p=BASE/'local'/name;p.write_text('\n'.join(f'{k}={value}' for k,value in values.items())+'\n');p.chmod(0o600)

if __name__=='__main__':
    v=settings();environments(v);schema(v);seed(v)
