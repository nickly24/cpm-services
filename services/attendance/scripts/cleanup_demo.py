"""Remove only redundant synthetic attendance API fixtures; preserve the latest demo."""
import json
from pathlib import Path
from http_test import Client
ROOT=Path(__file__).resolve().parents[3]
def main():
    demo=json.loads((ROOT/'dev/local/attendance-demo.json').read_text())
    admin=Client('dev_admin');foreign=demo['foreign_division_id']
    events=admin.get(f'/v1/attendance/schedule?division_ids=1001,1002,{foreign}&date_from=2026-11-15&date_to=2026-11-15')['events']
    keep=set(demo['lesson_ids'])
    staff=[e['id'] for e in events if e['lesson_name']=='DEV Attendance HTTP lesson' and e['division_id']==1002 and e['start_time']=='09:30']
    if staff:keep.add(max(staff))
    deletes=[{'id':e['id'],'division_id':e['division_id']} for e in events if e['id'] not in keep and (e['lesson_name']=='DEV Attendance HTTP lesson' or e['lesson_name'].startswith(('DEV UI schedule ','DEV UI edited ')))]
    if deletes:admin.put('/v1/attendance/schedule/bulk',{'creates':[],'updates':[],'deletes':deletes})
    days=admin.get('/v1/attendance/divisions/1001/class-days?date_from=2031-01-01&date_to=2031-01-31')
    removed=0
    for day in days:
        if day['comment'].startswith('DEV unknown outcome ') and not day['has_history']:
            admin.put(f"/v1/attendance/divisions/1001/class-days/{day['id']}",{},'DELETE');removed+=1
    print(f'Removed {len(deletes)} redundant DEV lessons and {removed} empty failed-test days through owner API.')
if __name__=='__main__':main()
