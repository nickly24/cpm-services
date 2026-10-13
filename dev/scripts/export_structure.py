"""Explicit, read-only export of database structure. Never reads application rows."""
import argparse
import datetime
import json
import os
import re
from pathlib import Path
import mysql.connector
from pymongo import MongoClient
from bson import json_util


def read_env(path):
    values = {}
    for line in Path(path).read_text().splitlines():
        key, sep, value = line.partition('=')
        if not sep or line.lstrip().startswith('#'):
            continue
        try:
            values[key.strip()] = json.loads(value)
        except ValueError:
            values[key.strip()] = value.strip().strip("'\"")
    return values


def export(source, dest):
    dest.mkdir(parents=True, exist_ok=True)
    conn = mysql.connector.connect(
        host=source['MYSQL_HOST'], port=int(source.get('MYSQL_PORT', 3306)),
        user=source['MYSQL_USER'], password=source['MYSQL_PASSWORD'],
        database=source['MYSQL_DATABASE'], autocommit=False,
        connection_timeout=15, ssl_disabled=False,
    )
    def query(sql):
        if not sql.startswith(('SHOW ', 'SELECT VERSION()', 'SELECT TABLE_', 'SELECT TRIGGER_', 'SELECT ROUTINE_', 'SELECT EVENT_')):
            raise ValueError('Only schema metadata queries are allowed')
        cur = conn.cursor(dictionary=True)
        try:
            cur.execute(sql)
            return cur.fetchall()
        finally:
            cur.close()
    try:
        conn.start_transaction(readonly=True)
        tables = query('SELECT TABLE_NAME,TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME')
        ddl = {}
        for row in tables:
            if row['TABLE_TYPE'] != 'BASE TABLE':
                raise ValueError('Views require separate review before import')
            name = row['TABLE_NAME']
            statement = query('SHOW CREATE TABLE `' + name.replace('`', '``') + '`')[0]['Create Table']
            statement = re.sub(r' AUTO_INCREMENT=\d+', '', statement)
            if re.search(r'REFERENCES\s+`[^`]+`\s*\.', statement, re.I):
                raise ValueError('Cross-database foreign key requires explicit mapping')
            ddl[name] = statement
        extra = {
            'triggers': query('SELECT TRIGGER_NAME FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=DATABASE()'),
            'routines': query('SELECT ROUTINE_NAME FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA=DATABASE()'),
            'events': query('SELECT EVENT_NAME FROM information_schema.EVENTS WHERE EVENT_SCHEMA=DATABASE()'),
        }
        mysql_meta = {'version':query('SELECT VERSION() AS version')[0]['version'], 'tables':tables, 'ddl':ddl, 'excluded_objects':extra}
    finally:
        conn.rollback()
        conn.close()
    client = MongoClient(source['MONGODB_URI'], serverSelectionTimeoutMS=15000, connectTimeoutMS=15000)
    try:
        db = client[source['MONGODB_DB_NAME']]
        version = client.server_info()['version']
        collections = []
        for item in db.list_collections():
            if item['name'].startswith('system.'):
                continue
            if item.get('type') != 'collection':
                raise ValueError('Mongo views require separate review before import')
            collections.append({'name':item['name'],'options':item.get('options',{}), 'indexes':list(db[item['name']].list_indexes())})
        mongo_meta = {'version':version,'collections':collections}
    finally:
        client.close()
    stamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
    mysql_meta['collected_at_utc'] = stamp
    mongo_meta['collected_at_utc'] = stamp
    files = {
        'mysql.json': json.dumps(mysql_meta,ensure_ascii=False,indent=2),
        'mongo.json': json_util.dumps(mongo_meta,ensure_ascii=False,indent=2),
        'mysql.sql':'-- Structure only; no application data, users, events or triggers.\nSET FOREIGN_KEY_CHECKS=0;\n\n'+'\n\n'.join(s+';' for s in ddl.values())+'\nSET FOREIGN_KEY_CHECKS=1;\n',
    }
    for name,content in files.items():
        p=dest/name
        p.write_text(content+'\n');p.chmod(0o600)
    print(json.dumps({'mysql_version':mysql_meta['version'],'mysql_tables':len(ddl),'excluded_objects':{k:len(v) for k,v in extra.items()},'mongo_version':version,'mongo_collections':len(collections),'application_rows_read':0},ensure_ascii=False))


if __name__ == '__main__':
    p=argparse.ArgumentParser();p.add_argument('--source-env',required=True,type=Path);p.add_argument('--output',type=Path,default=Path(__file__).resolve().parents[1]/'schema');args=p.parse_args()
    try:
        export(read_env(args.source_env),args.output)
    except Exception as e:
        print('Structure export failed:',type(e).__name__,'code:',getattr(e,'errno',getattr(e,'code',None)))
        raise SystemExit(1)
