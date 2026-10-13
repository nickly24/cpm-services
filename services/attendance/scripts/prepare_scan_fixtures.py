"""Two own native Attendance commands for the selected-day UI regression.

Run only in the Attendance owner's agreed mutation window. No Core, SQL, roles,
settings, broker or runtime changes. Fresh Auth session is always logged out.
"""
import argparse
import json
import secrets
import time
from pathlib import Path

from scope_http_test import Client, ROOT, http

OUTPUT = ROOT / 'dev/local/attendance-scan-fixtures.json'


def save(value):
    OUTPUT.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def run():
    fixture = json.loads((ROOT / 'dev/local/attendance-scope-fixtures.json').read_text())
    assert fixture['run'] == '0a242b5545' and fixture['d1'] == 10150
    assert fixture['student']['account_id'] == 391
    state = json.loads(OUTPUT.read_text()) if OUTPUT.exists() else {
        'run': fixture['run'], 'organization_id': 1001, 'division_id': fixture['d1'],
        'account_id': fixture['student']['account_id'], 'commands': {},
    }
    assert (state['run'], state['division_id'], state['account_id']) == (fixture['run'], 10150, 391)
    client = Client('dev_admin', 1001, 1001)
    prefix = '/divisions/' + str(fixture['d1'])
    try:
        days = client.read(prefix + '/class-days?date_from=2036-01-01&date_to=2036-01-02')
        first = next(day for day in days if day['date'] == '2036-01-01')
        assert first['id'] == fixture['days'][str(fixture['d1'])]['id']
        first_journal = client.read(prefix + '/class-days/' + str(first['id']) + '/attendance')
        assert not any(entry['account_id'] == state['account_id'] for entry in first_journal['entries'])
        types = client.read(prefix + '/attendance-types')
        active_type = next(t for t in types if t['code'] == 'scoped_excuse' and t['is_active'])
        assert active_type['name'] == 'DEV scope excuse ' + fixture['run']

        def command(label, path, data):
            if label not in state['commands']:
                client.read('/resources/rooms')
                state['commands'][label] = {
                    'path': path, 'data': data, 'expected_revision': client.rev,
                    'key': secrets.token_hex(16),
                }
                save(state)
            command = state['commands'][label]
            assert command['path'] == path and command['data'] == data
            value = client.write(path, data, revision=command['expected_revision'], key=command['key'])
            return value

        second = command('day', prefix + '/class-days', {
            'date': '2036-01-02', 'comment': 'DEV Attendance scan day ' + fixture['run'], 'lesson_ids': [],
        })
        assert second['division_id'] == state['division_id'] and second['date'] == '2036-01-02'
        entry = command('mark', prefix + '/class-days/' + str(second['id']) + '/attendance', {
            'account_id': state['account_id'], 'attendance_type_id': active_type['id'],
        })
        assert entry['account_id'] == state['account_id'] and entry['class_day_id'] == second['id']
        assert entry.get('zap_id') is None and entry['type']['name'] == active_type['name']
        actual = client.read(prefix + '/class-days/' + str(second['id']) + '/attendance')
        assert next(e for e in actual['entries'] if e['account_id'] == state['account_id']) == entry
        state.update({
            'status': 'prepared', 'verified_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
            'first_day': first, 'second_day': second, 'entry': entry, 'type': active_type,
            'native_commands': 2, 'core_writes': 0, 'runtime_changes': 0,
        })
        save(state)
        print('Prepared own two-day scan fixtures with exactly two native Attendance commands.')
    finally:
        http(8002, '/v1/auth/logout', 'POST', {}, client.token)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true')
    args = parser.parse_args()
    if not args.run:
        parser.error('use --run only in the agreed owner mutation window')
    run()
