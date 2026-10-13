"""Apply only the authorized guard migration in fixed local cpm_dev; preserve neighbours."""
import fcntl,json,sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from bootstrap import settings,mysql_connection
v=settings();conn=mysql_connection(v)
try:
    cur=conn.cursor();cur.execute('SELECT DATABASE()');assert cur.fetchone()[0]=='cpm_dev'
    for sql in (ROOT/'services/core/migrations/004_attendance_history_reservations.sql').read_text().split(';'):
        if sql.strip():cur.execute(sql)
    conn.commit();cur.close()
finally:conn.close()
with (ROOT/'dev/schema/extensions.json').open('r+') as f:
    fcntl.flock(f,fcntl.LOCK_EX);tables=set(json.load(f));tables.add('core_attendance_history_reservations')
    f.seek(0);json.dump(sorted(tables),f,indent=2);f.write('\n');f.truncate()
print('Only core permanent attendance-history marker migrated; existing data retained.')
