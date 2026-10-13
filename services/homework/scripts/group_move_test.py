"""Narrow real HTTP/PDF regression: current group and reviewer assignment.

Uses its own synthetic student/assignments. No pauses, broker topology changes,
settings changes, or edits to seeded users. Restores own membership then cleans
fixtures with the standard guarded local-dev cleanup.
"""
import urllib.error

from workflow_test import WorkflowSuite, pdf, FILEWORK


class GroupMoveSuite(WorkflowSuite):
    def __init__(self):
        super().__init__(chaos=False)
        self.prefix = 'DEV Homework GroupMove ' + self.run
        self.student_account = None

    def cleanup(self):
        try:
            if self.student_account is not None:
                self.core('/divisions/1001/groups/1001/members', self.admin, 'POST',
                          {'account_id': self.student_account})
        finally:
            super().cleanup()


def main(s):
    s.request('/health/ready')
    s.request('/readyz', origin=FILEWORK)
    s.admin, admin_actor = s.login('dev_admin')
    proctor1, _ = s.login('dev_proctor_1')
    proctor2, proctor2_actor = s.login('dev_proctor_2')
    student, actor, _ = s.fixture_student(group=1001)
    s.student_account = actor['account_id']
    kind = s.type()
    first, _ = s.assignment(kind['id'])
    _, workspace = s.process(first['id'], student, pdf(label='CPM moved student PDF'))
    submission = workspace['submission']['id']
    s.api(f'/assignments/{first["id"]}/submit', student, 'POST', {})
    s.api(f'/submissions/{submission}/claim', proctor1, 'POST', {})
    s.expect(s.row('SELECT state,reviewer_account_id FROM hw_submissions WHERE id=%s', (submission,))['state'] == 'in_review',
             'original group proctor claims submitted file')

    # Core changes only this own fixture's participation in the selected division.
    s.core('/divisions/1001/groups/1002/members', s.admin, 'POST', {'account_id': actor['account_id']})
    queue2 = s.api('/review-queue?assignment_id=' + str(first['id']), proctor2)
    s.expect(len(queue2['items']) == 1 and queue2['items'][0]['id'] == submission,
             'new group proctor immediately sees transferred student submission')
    transferred = queue2['items'][0]
    s.expect(transferred['state'] == 'submitted' and transferred['reviewer_account_id'] is None
             and transferred['group_id'] == 1002,
             'group move releases old proctor claim back to submitted')
    s.expect(not s.api('/review-queue?assignment_id=' + str(first['id']), proctor1)['items'],
             'former group proctor cannot list transferred submission')
    s.api(f'/submissions/{submission}/file-url', proctor1, code=403)
    s.api(f'/submissions/{submission}/grade', proctor1, 'POST', {'result': 80}, 403)
    s.api(f'/submissions/{submission}/claim', proctor1, 'POST', {}, 403)
    s.download(s.api(f'/submissions/{submission}/file-url', proctor2))
    s.api(f'/submissions/{submission}/claim', proctor2, 'POST', {})
    current = s.row('SELECT state,reviewer_account_id FROM hw_submissions WHERE id=%s', (submission,))
    s.expect(current['state'] == 'in_review' and current['reviewer_account_id'] == proctor2_actor['account_id'],
             'current group proctor becomes the sole reviewer')
    grade = s.api(f'/submissions/{submission}/grade', proctor2, 'POST', {'result': 87})
    s.expect(grade['result'] == 87, 'current group proctor can grade transferred homework')
    s.expect(len(s.api('/archive?assignment_id=' + str(first['id']), proctor2)['items']) == 1
             and not s.api('/archive?assignment_id=' + str(first['id']), proctor1)['items'],
             'archive access follows current student group')
    s.api(f'/submissions/{submission}/file-url', proctor1, code=403)
    s.download(s.api(f'/submissions/{submission}/file-url', proctor2))

    # An organization admin's inherited participation is a valid reviewer.
    # Queue reconciliation must not treat its absent group as a stale proctor.
    second, _ = s.assignment(kind['id'])
    _, workspace = s.process(second['id'], student, pdf(label='CPM administrator review PDF'))
    admin_submission = workspace['submission']['id']
    s.api(f'/assignments/{second["id"]}/submit', student, 'POST', {})
    s.api(f'/submissions/{admin_submission}/claim', s.admin, 'POST', {})
    admin_queue = s.api('/review-queue?assignment_id=' + str(second['id']), proctor2)
    s.expect(len(admin_queue['items']) == 1 and admin_queue['items'][0]['state'] == 'in_review'
             and admin_queue['items'][0]['reviewer_account_id'] == admin_actor['account_id'],
             'queue fetch preserves an inherited organization admin claim')
    s.api('/review-queue?assignment_id=' + str(second['id']), proctor1)
    current = s.row('SELECT state,reviewer_account_id FROM hw_submissions WHERE id=%s', (admin_submission,))
    s.expect(current['state'] == 'in_review' and current['reviewer_account_id'] == admin_actor['account_id'],
             'another proctor queue fetch cannot release organization admin reviewer')
    s.api(f'/submissions/{admin_submission}/grade', proctor2, 'POST', {'result': 88}, 409)
    s.api(f'/submissions/{admin_submission}/release', proctor2, 'POST', {}, 409)
    s.api(f'/submissions/{admin_submission}/grade', s.admin, 'POST', {'result': 92})
    s.expect(s.api(f'/assignments/{second["id"]}/workspace', student)['legacy_result']['result'] == 92,
             'admin can finish its retained review after group move')


if __name__ == '__main__':
    suite = GroupMoveSuite()
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
    suite.report('homework-group-move-report.json', 'failed' if failure else 'passed', failure)
    if failure:
        raise SystemExit(failure)
