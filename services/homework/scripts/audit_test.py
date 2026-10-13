"""Targeted real HTTP/PDF audit smoke against isolated loopback dev services.

Run only after the homework/Core/worker owners confirm their builds are ready:
  dev/.venv/bin/python services/homework/scripts/audit_test.py

Creates only synthetic fixtures, restores division settings, and uses the
standard guarded cleanup. Never pauses/restarts services or sends fault events.
The report contains assertion labels and public health state, never credentials,
object keys, tokens, presigned fields, signed URLs, or raw service responses.
"""
from datetime import datetime, timezone
import time
import urllib.error
import urllib.parse
import uuid

from http_test import AUTH
from workflow_test import WorkflowSuite, FILEWORK, pdf


PDF_LIMITS = ('max_source_bytes', 'max_output_bytes', 'max_pages', 'dpi', 'jpeg_quality')
SECTIONS = ('assignments', 'review-queue', 'homework-archive', 'monitoring')
HEALTH_STATES = {'ok', 'error', 'unknown', 'stale'}


class AuditSuite(WorkflowSuite):
    def __init__(self):
        super().__init__(chaos=False)
        self.prefix = 'DEV Homework Audit ' + self.run

    def proctor_session(self):
        account_id, group = self.fixture_proctor()
        own = self.row('SELECT username FROM auth_users WHERE id=%s', (account_id,))
        self.expect(own['username'].startswith('dev_homework_' + self.run + '_'),
                    'proctor session belongs to this synthetic run')
        token, actor = self.login(own['username'])
        return token, actor, group

    def credential_boundary(self):
        worker_key = self.local['HOMEWORK_SERVICE_KEY']
        core_key = self.env['AUTH_INTERNAL_KEY']
        self.expect(worker_key != core_key,
                    'worker and Core internal credentials are distinct')
        self.request('/internal/homework/ratings/snapshot', method='POST', body={}, code=401,
                     headers={'X-Service-Key': worker_key})
        self.request('/internal/homework/file-work/heartbeat', method='POST', body={}, code=401,
                     headers={'X-Service-Key': core_key})
        rejected = self.request('/internal/homework/file-jobs/' + str(uuid.uuid4()) + '/claim',
                                method='POST', body={'generation': 1}, code=403,
                                headers={'X-Service-Key': core_key})
        self.expect(rejected.get('error') == 'forbidden',
                    'Core credential cannot claim a worker job under the existing forbidden contract')
        rejected = self.request('/internal/homework/file-work/heartbeat', method='POST',
                                body={}, code=400, headers={'X-Service-Key': worker_key})
        self.expect(rejected.get('error') == 'invalid_heartbeat',
                    'authorized worker credential reaches heartbeat payload validation without recording a fake observer')

    def limits(self, job, expected, label):
        snapshot = job.get('limits')
        self.expect(isinstance(snapshot, dict) and set(snapshot) == set(PDF_LIMITS),
                    label + ': exposes exactly the five accepted PDF limits')
        self.expect(snapshot == {field: expected[field] for field in PDF_LIMITS},
                    label + ': PDF limits preserve the accepted job snapshot')
        self.expect(all(type(value) is int for value in snapshot.values()),
                    label + ': limit values are JSON integers')
        self.expect(not {'source_key', 'output_key', 'settings_json', 'claim_token'} & set(job),
                    label + ': private storage and worker lease fields are absent')

    def runtime(self, data, division, label):
        self.expect(data['organization_id'] == 1001 and data['division_id'] == division,
                    label + ': monitoring identifies the selected organization and division')
        self.expect(type(data.get('rabbitmq')) is bool
                    and type(data.get('processing_ready')) is bool,
                    label + ': broker and processing readiness are booleans')
        worker, storage = data.get('worker'), data.get('storage')
        self.expect(isinstance(worker, dict) and isinstance(storage, dict),
                    label + ': worker and storage observations are structured objects')
        self.expect(worker.get('state') in HEALTH_STATES
                    and storage.get('state') in HEALTH_STATES,
                    label + ': observations use documented health states')
        for field in ('received_at', 'observed_at'):
            self.expect(field in worker, label + ': worker exposes nullable ' + field)
            self.timestamp(worker[field], label + ': worker ' + field)
        self.expect('checked_at' in storage, label + ': storage exposes nullable checked_at')
        self.timestamp(storage['checked_at'], label + ': storage checked_at')
        for field in ('rabbitmq', 'tools', 'processing'):
            self.expect(field in worker and (worker[field] is None or type(worker[field]) is bool),
                        label + ': worker ' + field + ' is a nullable boolean')
        if worker['state'] == 'unknown':
            self.expect(worker['received_at'] is None and worker['observed_at'] is None
                        and all(worker[field] is None for field in ('rabbitmq', 'tools', 'processing')),
                        label + ': unknown worker does not invent an observation')
        if worker['state'] == 'ok':
            self.expect(worker['rabbitmq'] is True and worker['tools'] is True
                        and type(worker['processing']) is bool
                        and worker['received_at'] is not None and worker['observed_at'] is not None,
                        label + ': healthy worker has a real fresh broker/tool observation')
        if storage['state'] == 'unknown':
            self.expect(storage['checked_at'] is None,
                        label + ': unknown storage has no fabricated probe time')
        if storage['state'] in {'ok', 'error'}:
            self.expect(storage['checked_at'] is not None,
                        label + ': observed storage state has a probe timestamp')
        expected = data['rabbitmq'] and worker['state'] == 'ok' and storage['state'] == 'ok'
        self.expect(data['processing_ready'] is expected,
                    label + ': processing readiness requires broker, worker and storage health')
        self.expect(not {'url', 'bucket', 'access_key', 'secret', 'instance_id'} & set(worker)
                    and not {'url', 'bucket', 'access_key', 'secret', 'object_key'} & set(storage),
                    label + ': infrastructure observation contains no storage secrets or object identity')

    def timestamp(self, value, label):
        if value is None:
            self.expect(True, label + ' may be null before an observation')
            return
        self.expect(isinstance(value, str), label + ' is an RFC3339 string')
        try:
            at = datetime.fromisoformat(value.replace('Z', '+00:00'))
        except ValueError:
            raise AssertionError(label + ' must parse as RFC3339') from None
        self.expect(at.tzinfo is not None and at.utcoffset().total_seconds() == 0
                    and (at - datetime.now(timezone.utc)).total_seconds() <= 5,
                    label + ' carries UTC and is not an invented future observation')

    def report_query(self, deadline, **filters):
        return '/report?' + urllib.parse.urlencode({
            'date_from': deadline, 'date_to': deadline, **filters,
        })

    def assert_report(self, data, division, assignment_id, student_id, score=None):
        self.expect(data['organization_id'] == 1001 and data['division_id'] == division,
                    'report identifies the organization and requested division')
        self.expect({item['id'] for item in data['assignments']} == {assignment_id},
                    'report type filter selects only its own division assignment')
        self.expect(len(data['students']) == 1 and data['students'][0]['account_id'] == student_id,
                    'report account filter selects the own synthetic student')
        self.expect('class' in data['students'][0] and data['students'][0]['class'] == 10,
                    'Core class10 survives the homework report projection')
        self.expect(all('class' in item and (item['class'] is None or type(item['class']) is int)
                        for item in data['students']),
                    'report class field has the documented nullable integer contract')
        if score is not None:
            marks = {item['assignment_id']: item['result'] for item in data['students'][0]['results']}
            self.expect(marks[assignment_id]['result'] == score,
                        'report retains the grade recorded through the PDF review workflow')


def main(s):
    s.request('/health/ready')
    s.request('/readyz', origin=FILEWORK)
    s.credential_boundary()
    s.admin, _ = s.login('dev_admin')
    foreign_org, foreign_actor = s.login('dev_admin_math', 2001, 2001)
    proctor, _, own_group = s.proctor_session()
    other_proctor, _, other_group = s.proctor_session()
    student, actor, _ = s.fixture_student(own_group)
    other_student, other_actor, _ = s.fixture_student(other_group)
    viewer, editor, roles = s.fixture_staff()
    start_audit = s.row('SELECT COALESCE(MAX(id),0) id FROM hw_audit WHERE organization_id=1001 AND division_id=1001')['id']

    kind = s.type()
    other_kind = s.type(division=1002, token=editor, name=kind['name'])
    assignment, body = s.assignment(kind['id'])
    other_assignment, other_body = s.assignment(other_kind['id'], division=1002, token=editor)
    deadline, account = body['deadline'], actor['account_id']
    own_report = s.report_query(deadline, type_id=kind['id'], account_id=account)
    reserve_report = s.report_query(deadline, type_id=other_kind['id'])

    # Existing public rights: settings changes and audit remain full-admin only.
    first_settings = s.save_settings()
    reserve_settings = s.save_settings(1002)
    for token, division, report in ((s.admin, 1001, own_report), (viewer, 1001, own_report),
                                    (editor, 1002, reserve_report), (student, 1001, own_report),
                                    (proctor, 1001, own_report)):
        s.api('/settings', token, division=division)
        data = s.api(report, token, division=division)
        if division == 1001:
            s.assert_report(data, 1001, assignment['id'], account)
        else:
            s.expect(data['organization_id'] == 1001 and data['division_id'] == 1002,
                     'same staff account sees the reserve division report context')
    for token, division, config in ((viewer, 1001, first_settings), (editor, 1002, reserve_settings),
                                     (student, 1001, first_settings), (proctor, 1001, first_settings)):
        s.api('/settings', token, 'PUT', config, 403, division=division)
        s.api('/audit', token, code=403, division=division)
    s.api('/audit', s.admin)
    for token, division in ((s.admin, 1001), (viewer, 1001), (editor, 1002)):
        s.runtime(s.api('/monitoring', token, division=division), division, 'initial monitoring')
    for token in (student, proctor):
        s.api('/monitoring', token, code=403)

    # Context is authoritative; a single staff account has different live rights.
    s.api('/assignments/' + str(assignment['id']), viewer, 'PUT', body, 403)
    s.api('/assignments/' + str(other_assignment['id']), editor, 'PUT', other_body, division=1002)
    old_viewer = viewer
    switched = s.request('/v1/auth/context', viewer, 'POST',
                         {'organization_id': 1001, 'division_id': 1002}, origin=AUTH)
    s.sessions.append(switched['token'])
    s.expect(switched['user']['account_id'] == s.users[-1]
             and switched['user']['division_id'] == 1002 and switched['user']['role_id'] == roles[1],
             'same synthetic staff account switches to its editor role in division1002')
    s.request('/v1/auth/me', old_viewer, code=401, origin=AUTH)
    s.api('/settings', switched['token'], division=1002)
    s.api('/settings', switched['token'], code=404)
    returned = s.request('/v1/auth/context', switched['token'], 'POST',
                        {'organization_id': 1001, 'division_id': 1001}, origin=AUTH)
    viewer = returned['token']
    s.sessions.append(viewer)
    s.expect(returned['user']['account_id'] == switched['user']['account_id']
             and returned['user']['role_id'] == roles[0],
             'returning to division1001 restores the same account viewer role')
    s.request('/v1/auth/me', switched['token'], code=401, origin=AUTH)
    s.api('/assignments/' + str(assignment['id']), viewer, 'PUT', body, 403)

    # Scope failures use own foreign-division fixtures; no foreign data is edited.
    for path in ('/settings', '/audit', '/monitoring', own_report):
        s.api(path, foreign_org, code=404)
        s.api(path, viewer, code=404, division=1002)
        s.api(path, s.admin, code=404, division=2001)
    s.api(own_report, student, code=404, division=1002)
    for path in ('/assignments/' + str(other_assignment['id']),
                 '/assignments/' + str(other_assignment['id']) + '/workspace'):
        s.api(path, s.admin, code=404)
    s.api('/assignments/' + str(assignment['id']), editor, code=404, division=1002)
    s.api('/assignments?type_id=' + str(other_kind['id']), viewer, code=404)
    s.api(s.report_query(deadline, type_id=other_kind['id']), s.admin, code=404)
    s.api(s.report_query(deadline, type_id=kind['id']), editor, code=404, division=1002)
    s.api(s.report_query(deadline, account_id=account), editor, code=404, division=1002)
    s.api(s.report_query(deadline, account_id=foreign_actor['account_id']), s.admin, code=404)
    s.api(s.report_query(deadline, account_id=other_actor['account_id']), student, code=403)
    s.api(s.report_query(deadline, account_id=other_actor['account_id']), proctor, code=403)
    s.api(s.report_query(deadline, group_id=own_group), editor, code=404, division=1002)
    for field in ('account_id', 'type_id', 'group_id'):
        s.api(s.report_query(deadline, **{field: -1}), s.admin, code=400)

    # All five limits change after job creation; the accepted two-page file must
    # still finish with its immutable two-page policy, while current settings=1.
    accepted = {**first_settings, 'max_source_bytes': 2 * 1024 * 1024,
                'max_output_bytes': 2 * 1024 * 1024, 'max_pages': 2, 'dpi': 144, 'jpeg_quality': 80}
    changed = {**first_settings, 'max_source_bytes': 1024 * 1024,
               'max_output_bytes': 1024 * 1024, 'max_pages': 1, 'dpi': 96, 'jpeg_quality': 60}
    reserve = {**reserve_settings, 'max_source_bytes': 3 * 1024 * 1024,
               'max_output_bytes': 4 * 1024 * 1024, 'max_pages': 7, 'dpi': 240, 'jpeg_quality': 90}
    s.api('/settings', s.admin, 'PUT', accepted)
    s.expect(s.api('/settings', s.admin, division=1002) == reserve_settings,
             'changing division1001 settings preserves division1002 settings')
    s.api('/settings', s.admin, 'PUT', reserve, division=1002)
    s.expect(s.api('/settings', student) == accepted,
             'changing division1002 settings preserves division1001 settings')
    upload = s.new_upload(assignment['id'], student)
    job_id = upload['job']['id']
    s.limits(upload['job'], accepted, 'created upload job')
    s.expect(upload['max_bytes'] == accepted['max_source_bytes'],
             'presigned upload announces accepted source-byte snapshot')
    s.api('/settings', s.admin, 'PUT', changed)
    s.expect(s.api('/settings', student) == changed and s.api('/settings', editor, division=1002) == reserve,
             'current settings change independently in each selected division')
    s.limits(s.api('/jobs/' + job_id, student), accepted, 'polling upload job after settings change')
    active = s.api('/jobs/active', student)
    own_job = [job for job in active['items'] if job['id'] == job_id]
    s.expect(len(own_job) == 1, 'active upload list contains its own private job once')
    s.limits(own_job[0], accepted, 'active upload job after settings change')
    s.expect(job_id not in {job['id'] for job in s.api('/jobs/active', other_student)['items']},
             'another student active-job list cannot disclose the own job')
    s.api('/jobs/' + job_id, other_student, code=404)
    s.api('/jobs/' + str(uuid.uuid4()), student, code=404)
    for token in (s.admin, viewer, proctor):
        s.api('/jobs/' + job_id, token, code=403)
    s.api('/jobs/' + job_id, foreign_org, code=404)
    s.api('/jobs/' + job_id, editor, code=403, division=1002)
    content = pdf(pages=2, label='CPM own audit immutable policy')
    s.upload_post(upload['upload'], content)
    s.limits(s.complete(upload, student), accepted, 'completed source job after settings change')
    ready = s.wait_job(job_id, student)
    s.limits(ready, accepted, 'ready job after actual worker compression')
    workspace = s.api('/assignments/' + str(assignment['id']) + '/workspace', student)
    submission = workspace['submission']['id']
    s.expect(workspace['limits']['max_pages'] == 1
             and workspace['submission']['draft_file']['page_count'] == 2,
             'actual worker accepts the original two-page snapshot despite current one-page limit')

    # Draft is private, then only the current account/group/section can download.
    s.download(s.api('/submissions/' + str(submission) + '/file-url?draft=1', student))
    for token in (s.admin, viewer, proctor, other_proctor, other_student):
        s.api('/submissions/' + str(submission) + '/file-url?draft=1', token, code=403)
    staff_workspace = s.api('/assignments/' + str(assignment['id']) + '/workspace?account_id=' + str(account), viewer)
    s.expect(staff_workspace['submission']['state'] == 'none'
             and staff_workspace['submission'].get('draft_file') is None
             and staff_workspace['submission']['has_draft'] is False
             and staff_workspace['active_job'] is None,
             'staff workspace hides private pre-submit draft and job')
    s.api('/assignments/' + str(assignment['id']) + '/submit', student, 'POST', {})
    for token in (student, viewer, proctor):
        s.download(s.api('/submissions/' + str(submission) + '/file-url', token))
    for token in (other_student, other_proctor):
        s.api('/submissions/' + str(submission) + '/file-url', token, code=403)
    s.api('/submissions/' + str(submission) + '/file-url', editor, code=404, division=1002)
    s.api('/submissions/' + str(submission) + '/file-url', foreign_org, code=404)
    s.api('/submissions/9223372036854770000/file-url', s.admin, code=404)
    s.api('/submissions/' + str(submission) + '/claim', proctor, 'POST', {})
    s.api('/submissions/' + str(submission) + '/grade', proctor, 'POST', {'result': 85})
    s.assert_report(s.api(own_report, student), 1001, assignment['id'], account, 85)
    s.download(s.api('/submissions/' + str(submission) + '/file-url', viewer))

    next_assignment, _ = s.assignment(kind['id'])
    next_upload = s.new_upload(next_assignment['id'], student)
    s.limits(next_upload['job'], changed, 'new job accepts only the updated five limits')
    s.api('/jobs/' + next_upload['job']['id'] + '/cancel', student, 'POST', {})

    # Existing access tokens must observe live scoped role revocation immediately.
    s.core('/roles/' + str(roles[0]), s.admin, 'PUT', {
        'name': s.prefix + ' viewer',
        'permissions': {section: {'view': False, 'edit': False} for section in SECTIONS},
    })
    for path in ('/settings', own_report, '/monitoring', '/audit',
                 '/submissions/' + str(submission) + '/file-url'):
        s.api(path, viewer, code=403)
    s.api('/settings', editor, division=1002)
    s.api(reserve_report, editor, division=1002)
    s.api('/assignments/' + str(other_assignment['id']), editor, 'PUT', other_body, division=1002)
    s.expect(True, 'revoking division1001 viewer rights preserves the same account division1002 editor rights')
    s.core('/roles/' + str(roles[1]), s.admin, 'PUT', {
        'name': s.prefix + ' editor',
        'permissions': {section: {'view': True, 'edit': False} for section in SECTIONS},
    })
    s.api('/assignments/' + str(other_assignment['id']), editor, 'PUT', other_body, 403, division=1002)
    s.runtime(s.api('/monitoring', editor, division=1002), 1002, 'live read-only editor monitoring')
    s.api('/settings', editor, 'PUT', reserve, 403, division=1002)

    audit = s.api('/audit?after=' + str(start_audit) + '&limit=100', s.admin)
    own_upload_audit = [item for item in audit['items'] if item['action'] == 'upload.create'
                        and str(item['entity_id']) == job_id]
    s.expect(len(own_upload_audit) == 1, 'audit preserves UUID upload entity without numeric coercion')
    s.expect(all(set(item) <= {'id', 'actor_id', 'action', 'entity_id', 'created_at'} for item in audit['items']),
             'audit response exposes history fields without credentials or storage URLs')
    reserve_audit = s.api('/audit?limit=100', s.admin, division=1002)
    s.expect(not any(str(item['entity_id']) == job_id for item in reserve_audit['items']),
             'division1002 audit cannot expose division1001 upload history')

    # A genuine worker result proves processing separately from reported readiness.
    until = time.monotonic() + 30
    while True:
        health = s.api('/monitoring', s.admin, record=False)
        if health.get('processing_ready') is True or time.monotonic() >= until:
            break
        time.sleep(.4)
    s.runtime(health, 1001, 'monitoring after actual PDF processing')
    s.expect(health['processing_ready'] is True and health['worker']['state'] == 'ok'
             and health['storage']['state'] == 'ok',
             'healthy local broker, actual worker heartbeat and storage probe report processing ready')
    s.metrics = {'real_pdf_pages': 2, 'original_job_limits_immutable': True,
                 'internal_credential_boundary_verified': True,
                 'claim_invalid_service_key_status': 403,
                 'same_staff_context_switch_verified': True,
                 'live_role_revocation_verified': True,
                 'monitoring_worker_state': health['worker']['state'],
                 'monitoring_storage_state': health['storage']['state'],
                 'monitoring_processing_ready': health['processing_ready'],
                 'shared_services_paused_or_restarted': False}


if __name__ == '__main__':
    suite = AuditSuite()
    failure = None
    try:
        main(suite)
    except Exception as exc:
        if isinstance(exc, AssertionError):
            failure = str(exc)
        elif isinstance(exc, urllib.error.URLError):
            failure = 'URLError: ' + type(exc.reason).__name__
        else:
            failure = type(exc).__name__
    finally:
        try:
            suite.cleanup()
        except Exception as exc:
            failure = failure or 'cleanup failed: ' + type(exc).__name__
    suite.report('homework-audit-report.json', 'failed' if failure else 'passed', failure)
    if failure:
        raise SystemExit(failure)
