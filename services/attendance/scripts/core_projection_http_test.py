"""Read Core -> Attendance student projections after the coordinated Core rollout.

Run --verify for class/group/report reads. Add --allow-school-profile-fixtures to
create/reuse two own synthetic schools and bind only the scope fixture students.
The class=10/group-name projection is checked BEFORE any school/profile mutation.

Requires real Auth/Core/Attendance on 8002/8003/8013, the additive Core reference
projection, migration 005_core_scope_guards.sql, and CORE_REPORTS_ATTENDANCE_URL.
Uses attendance-scope-fixtures.json; writes a separate token-free report only.
No SQL, broker, runtime restart, attendance mutation or production connection.
"""
import argparse
import json
import time
from urllib.parse import quote

from scope_http_test import Client, FIXTURES, ROOT, checks, expect

REPORT = ROOT / 'dev/local/attendance-core-projection-http-report.json'
PROJECTION_FIXTURES = ROOT / 'dev/local/attendance-core-projection-fixtures.json'
PERIOD = '?date_from=2036-01-01&date_to=2036-01-01'


def own_profile(client, division, student, run):
    profile = client.core(f'/divisions/{division}/users/{student["account_id"]}')
    expect(profile['account_id'] == student['account_id']
           and profile['login'] == student['login']
           and profile['login'].startswith('dev_att_scope_' + run + '_')
           and profile['role'] == 'student'
           and profile['class'] == 10,
           'profile is the own synthetic class-10 scope student')
    return profile


def student_row(rows, account):
    matches = [row for row in rows if row['account_id'] == account]
    expect(len(matches) == 1, 'own student appears exactly once in projection')
    return matches[0]


def check_metadata(row, group, group_name, school=None, short_name=True):
    expect(row.get('class') == 10, 'actual class=10 reaches projection; no null fallback')
    expect(row.get('group_id') == group and row.get('group_name') == group_name,
           'division-specific group ID and name reach projection')
    if school is not None:
        expect(row.get('school_id') == school['id']
               and row.get('school_name') == school['name'],
               'scoped school ID and name reach projection')
        if short_name:
            expect(row.get('school_short_name') == school['short_name'],
                   'scoped school short name reaches Member projection')


def profile_payload(profile, school_id):
    # PUT ProfileDTO replaces these fields. Preserve the existing synthetic
    # name/class/tg_name; membership/group/login are not profile input fields.
    return {'full_name': profile['full_name'], 'class': profile['class'],
            'tg_name': profile.get('tg_name') or '', 'school_id': school_id}


def own_school(client, run, suffix):
    data = {'name': 'DEV Attendance projection ' + run,
            'short_name': 'ATT-' + suffix + '-' + run,
            'notes': 'Own Attendance projection fixture ' + run,
            'is_active': True}
    found = [row for row in client.core('/schools?limit=200&q=' + quote(data['name']))
             if row['name'] == data['name']]
    expect(len(found) <= 1, 'same school name is unique within selected organization')
    if found:
        school = found[0]
        expect(all(school.get(key) == value for key, value in data.items()),
               'existing school has own fixture marker and expected metadata')
    else:
        school_id = client.core('/schools', data, want=201)['id']
        school = client.core(f'/schools/{school_id}')
        expect(all(school.get(key) == value for key, value in data.items()),
               'own school created with the exact Core SchoolDTO contract')
    return school


def verify(allow_school_fixtures):
    f = json.loads(FIXTURES.read_text())
    run, d1, d2, md = f['run'], f['d1'], f['d2'], f['math_division']
    u, mu = f['student']['account_id'], f['math_student']['account_id']
    a, m = Client('dev_admin', 1001, 1001), Client('dev_admin_math', 2001, 2001)
    s, ms = Client(f['student']['login'], 1001, d1), Client(f['math_student']['login'], 2001, md)
    group_name = 'DEV Attendance scope group ' + run
    profile, math_profile = own_profile(a, d1, f['student'], run), own_profile(m, md, f['math_student'], run)

    # Old Core runtimes make the newly nullable Member.class null. Stop here,
    # before creating a school or changing either synthetic student profile.
    check_metadata(student_row(a.read(f'/divisions/{d1}/students'), u), f['group'], group_name)
    check_metadata(student_row(s.read(f'/divisions/{d1}/students'), u), f['group'], group_name)
    schools = None
    if allow_school_fixtures:
        own, foreign = own_school(a, run, 'A'), own_school(m, run, 'M')
        expect(own['id'] != foreign['id'] and own['name'] == foreign['name'],
               'equal school names in two organizations remain distinct scoped records')
        for client, div, current, school in [(a, d1, profile, own), (m, md, math_profile, foreign)]:
            expect(current.get('school_id') in (None, school['id']),
                   'fixture update will not replace an unrelated school binding')
            client.core(f'/divisions/{div}/users/{current["account_id"]}',
                        profile_payload(current, school['id']), 'PUT')
        schools = {'1001': own, '2001': foreign}
        PROJECTION_FIXTURES.write_text(json.dumps({'run': run, 'schools': schools}, ensure_ascii=False, indent=2) + '\n')
        # Negative PUT is gated by the same explicit fixture-mutation flag.
        a.core(f'/divisions/{d1}/users/{u}', profile_payload(profile, foreign['id']), 'PUT', want=404)
        expect(a.core(f'/divisions/{d1}/users/{u}')['school_id'] == own['id'],
               'foreign-org school update is rejected without changing own profile')
    elif PROJECTION_FIXTURES.exists():
        saved = json.loads(PROJECTION_FIXTURES.read_text())
        if saved.get('run') == run:
            schools = saved['schools']

    own, foreign = (schools['1001'], schools['2001']) if schools else (None, None)
    for client in (a, s):
        rows = client.read(f'/divisions/{d1}/students')
        check_metadata(student_row(rows, u), f['group'], group_name, own)
        expect(all(row['account_id'] != mu for row in rows), 'other organization student absent from own roster')
        if client is s:
            expect([row['account_id'] for row in rows] == [u], 'student roster contains only the requesting student')
    check_metadata(student_row(a.read(f'/divisions/{d2}/students'), u), None, None, own)
    check_metadata(student_row(ms.read(f'/divisions/{md}/students'), mu), None, None, foreign)
    for client, div, account, group, name, school, org in [
            (a, d1, u, f['group'], group_name, own, 1001),
            (m, md, mu, None, None, foreign, 2001)]:
        report = client.core(f'/divisions/{div}/reports/attendance{PERIOD}')
        expect(report['organization_id'] == org and report['division_id'] == div,
               'Core attendance report preserves selected organization/division')
        check_metadata(student_row(report['students'], account), group, name, school)
        owner = client.read(f'/divisions/{div}/attendance-report{PERIOD}')
        expected_day = f['days'][str(div)]['id']
        expect(owner['organization_id'] == org and owner['division_id'] == div
               and any(day['id'] == expected_day for day in owner['days'])
               and any(item['id'] == str(expected_day) for item in report['items']),
               'nonempty Core report uses the real scoped Attendance day, not a stub')
        expect(all(row['account_id'] != (mu if org == 1001 else u) for row in report['students']),
               'Core report roster excludes other organization student')
    a.read(f'/divisions/{md}/students', 404)
    m.read(f'/divisions/{d1}/students', 404)
    a.core(f'/divisions/{md}/reports/attendance{PERIOD}', want=404)
    m.core(f'/divisions/{d1}/reports/attendance{PERIOD}', want=404)
    s.read(f'/divisions/{md}/students', 404)
    if schools:
        a.core(f'/schools/{foreign["id"]}', want=404)
        m.core(f'/schools/{own["id"]}', want=404)
    return {'status': 'passed' if schools else 'partial',
            'verified_at': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
            'checks': checks, 'fixture_run': run, 'organizations': [1001, 2001],
            'divisions': [d1, d2, md],
            'school_projection': 'passed' if schools else 'deferred_use_allow_school_profile_fixtures',
            'mutations_enabled': allow_school_fixtures,
            'dependencies': ['actual auth', 'actual Core reference projection', 'actual Attendance', 'actual MySQL']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--verify', action='store_true')
    parser.add_argument('--allow-school-profile-fixtures', action='store_true',
                        help='explicitly create/reuse own scoped schools and update only own synthetic student profiles')
    args = parser.parse_args()
    if not args.verify:
        parser.error('choose --verify; school/profile writes additionally require --allow-school-profile-fixtures')
    try:
        report = verify(args.allow_school_profile_fixtures)
    except (AssertionError, KeyError, ValueError, TypeError, OSError) as error:
        REPORT.write_text(json.dumps({'status': 'failed', 'checks': checks,
                                      'error': str(error), 'mutations_enabled': args.allow_school_profile_fixtures},
                                     ensure_ascii=False, indent=2) + '\n')
        raise SystemExit('Projection checks failed; see separate token-free attendance-core-projection-http-report.json.')
    REPORT.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    print(f"Core projection checks {report['status']}: {len(checks)}; school projection {report['school_projection']}.")


if __name__ == '__main__':
    main()
