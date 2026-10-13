"""Real S3/Rabbit/PDF/workflow checks for local synthetic homework fixtures.

Normal suite leaves all services running. --chaos adds controlled pause/unpause
of local worker, S3 and broker, so run it in a serial integration-test window.
No production endpoint is accepted and no secrets are included in reports.
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from datetime import datetime, timezone
import hashlib
import hmac
import json
from pathlib import Path
import re
import secrets
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
import zlib
from zoneinfo import ZoneInfo

from http_test import Suite, ROOT, HOMEWORK, FILEWORK, mysql_connection
from export_structure import read_env


def pdf(pages=1, *, image=False, active=False, encrypted=False, label='CPM Homework readable text'):
    """Small standards-compliant PDF generator, no external Python package needed."""
    objects = []
    def add(data):
        objects.append(data.encode() if isinstance(data, str) else data)
        return len(objects)
    add('')  # catalogue, filled after references are allocated
    add('')  # page tree
    font = add('<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>')
    image_ref = None
    if image:
        # 1200x1600 raw RGB scan, approximately 5.5 MiB. Gradients remain readable
        # after the actual Ghostscript image downsampling/JPEG pass.
        width, height = 1200, 1600
        pixels = bytearray(width * height * 3)
        for y in range(height):
            for x in range(width):
                pos = (y * width + x) * 3
                pixels[pos:pos+3] = bytes(((x * 255 // width), (y * 255 // height), ((x + y) % 256)))
        image_ref = add(f'<< /Type /XObject /Subtype /Image /Width {width} /Height {height} /ColorSpace /DeviceRGB /BitsPerComponent 8 /Length {len(pixels)} >>\nstream\n'.encode() + pixels + b'\nendstream')
    children = []
    for index in range(pages):
        text = (label + ' page ' + str(index + 1)).replace('\\', '\\\\').replace('(', '\\(').replace(')', '\\)')
        content = (f'q 535 0 0 740 30 30 cm /Im1 Do Q\n' if image else '') + f'BT /F1 18 Tf 40 790 Td ({text}) Tj ET'
        stream = add(f'<< /Length {len(content.encode())} >>\nstream\n{content}\nendstream')
        resources = f'/Font << /F1 {font} 0 R >>' + (f' /XObject << /Im1 {image_ref} 0 R >>' if image else '')
        children.append(add(f'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << {resources} >> /Contents {stream} 0 R >>'))
    info = add('<< /Author (DEV_PRIVATE_AUTHOR_REMOVE) /Title (DEV_PRIVATE_TITLE_REMOVE) >>')
    encrypt_ref, file_id, encryption_key = None, None, None
    if encrypted:
        # Standard PDF revision2 RC4 encryption. This is a valid password-protected
        # fixture, not merely a corrupt file with an /Encrypt flag.
        padding = bytes.fromhex('28bf4e5e4e758a4164004e56fffa01082e2e00b6d0683e802f0ca9fe6453697a')
        user_password = (b'dev-fixture-password' + padding)[:32]
        owner_password = (b'dev-fixture-owner' + padding)[:32]
        owner = rc4(hashlib.md5(owner_password).digest()[:5], user_password)
        file_id = hashlib.md5(b'CPM synthetic encrypted PDF fixture').digest()
        encryption_key = hashlib.md5(user_password + owner + struct.pack('<i', -4) + file_id).digest()[:5]
        user = rc4(encryption_key, padding)
        encrypt_ref = add('<< /Filter /Standard /V 1 /R 2 /O <' + owner.hex() + '> /U <' + user.hex() + '> /P -4 >>')
    action = r' /OpenAction << /S /JavaScript /JS (app.alert\(1\)) >>' if active else ''
    objects[0] = f'<< /Type /Catalog /Pages 2 0 R{action} >>'.encode()
    objects[1] = f'<< /Type /Pages /Count {pages} /Kids [{" ".join(str(child) + " 0 R" for child in children)}] >>'.encode()
    if encrypted:
        for index, obj in enumerate(objects, 1):
            object_key = hashlib.md5(encryption_key + struct.pack('<I', index)[:3] + b'\x00\x00').digest()[:10]
            start = obj.find(b'stream\n')
            if start >= 0:
                start += len(b'stream\n')
                end = obj.rfind(b'\nendstream')
                objects[index-1] = obj[:start] + rc4(object_key, obj[start:end]) + obj[end:]
            elif index == info:
                objects[index-1] = re.sub(rb'\(([^()]*)\)', lambda match: b'<' + rc4(object_key, match[1]).hex().encode() + b'>', obj)
    output = bytearray(b'%PDF-1.7\n%\xe2\xe3\xcf\xd3\n')
    offsets = [0]
    for index, obj in enumerate(objects, 1):
        offsets.append(len(output))
        output.extend(f'{index} 0 obj\n'.encode() + obj + b'\nendobj\n')
    xref = len(output)
    output.extend(f'xref\n0 {len(objects)+1}\n0000000000 65535 f \n'.encode())
    for offset in offsets[1:]:
        output.extend(f'{offset:010d} 00000 n \n'.encode())
    encryption = f' /Encrypt {encrypt_ref} 0 R /ID [<{file_id.hex()}><{file_id.hex()}>]' if encrypted else ''
    output.extend(f'trailer\n<< /Size {len(objects)+1} /Root 1 0 R /Info {info} 0 R{encryption} >>\nstartxref\n{xref}\n%%EOF\n'.encode())
    return bytes(output)


def rc4(key, content):
    state = list(range(256))
    j = 0
    for i in range(256):
        j = (j + state[i] + key[i % len(key)]) % 256
        state[i], state[j] = state[j], state[i]
    i = j = 0
    output = bytearray()
    for byte in content:
        i = (i + 1) % 256
        j = (j + state[i]) % 256
        state[i], state[j] = state[j], state[i]
        output.append(byte ^ state[(state[i] + state[j]) % 256])
    return bytes(output)


def readable_pdf_text(content):
    """Inspect real PDF content streams, including QPDF's Flate compressed streams.

    The generated fixture uses standard Helvetica; strings remain ASCII when
    text survives as PDF text rather than being rasterized into page images.
    """
    streams = []
    for data in re.findall(rb'(?<!end)stream\r?\n(.*?)\r?\nendstream', content, re.S):
        try:
            data = zlib.decompress(data)
        except zlib.error:
            pass
        streams.append(data)
    return b'\n'.join(streams)


class WorkflowSuite(Suite):
    def __init__(self, chaos=False):
        super().__init__('Workflow')
        self.chaos = chaos
        self.paused = set()
        self.local = read_env(ROOT / 'dev/local/homework-compose.env')
        endpoint = self.local.get('HOMEWORK_S3_PUBLIC_ENDPOINT_URL', '')
        parsed = urllib.parse.urlsplit(endpoint)
        if parsed.scheme != 'http' or parsed.hostname != '127.0.0.1' or parsed.port != 19000:
            raise AssertionError('Workflow tests require the isolated loopback S3 fixture on19000')
        if self.local.get('HOMEWORK_S3_BUCKET') != 'cpm-homework-test':
            raise AssertionError('Workflow tests require the synthetic cpm-homework-test bucket')
        self.s3_endpoint = endpoint.rstrip('/')
        self.s3_bucket = self.local['HOMEWORK_S3_BUCKET']

    @staticmethod
    def local_url(url):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme != 'http' or parsed.hostname != '127.0.0.1' or parsed.port != 19000:
            raise AssertionError('Presigned URL must target only the isolated local S3 fixture')

    def upload_post(self, upload, content, *, fields_override=None, codes=(200, 201, 204)):
        self.local_url(upload['url'])
        boundary = 'cpm-homework-' + secrets.token_hex(10)
        fields = dict(upload['fields'])
        fields.update(fields_override or {})
        parts = []
        for name, value in fields.items():
            parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"\r\n\r\n{value}\r\n'.encode())
        parts.append(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="synthetic.pdf"\r\nContent-Type: application/pdf\r\n\r\n'.encode() + content + b'\r\n')
        parts.append(f'--{boundary}--\r\n'.encode())
        req = urllib.request.Request(upload['url'], data=b''.join(parts), method='POST',
                                     headers={'Content-Type': 'multipart/form-data; boundary=' + boundary})
        try:
            with urllib.request.urlopen(req, timeout=45) as response:
                status = response.status
                response.read()
        except urllib.error.HTTPError as error:
            status = error.code
            error.read()
        self.expect(status in codes, 'browser directly uploads PDF to local S3: HTTP' + str(status))

    def signed_s3(self, method, key, content=b'', metadata=None, expected=(200, 204)):
        """Direct local fixture write used only to test wrong owner / changed ETag."""
        region = self.local.get('HOMEWORK_S3_REGION', 'us-east-1')
        access = self.local['HOMEWORK_S3_ACCESS_KEY_ID']
        secret = self.local['HOMEWORK_S3_SECRET_ACCESS_KEY']
        now = datetime.now(timezone.utc)
        date, timestamp = now.strftime('%Y%m%d'), now.strftime('%Y%m%dT%H%M%SZ')
        host = urllib.parse.urlsplit(self.s3_endpoint).netloc
        digest = hashlib.sha256(content).hexdigest()
        headers = {'host': host, 'x-amz-date': timestamp, 'x-amz-content-sha256': digest}
        headers.update({'x-amz-meta-' + name: str(value) for name, value in (metadata or {}).items()})
        names = ';'.join(sorted(headers))
        canonical_headers = ''.join(name + ':' + headers[name].strip() + '\n' for name in sorted(headers))
        path = '/' + urllib.parse.quote(self.s3_bucket, safe='') + '/' + urllib.parse.quote(key, safe='/~-_.')
        canonical = '\n'.join([method, path, '', canonical_headers, names, digest])
        scope = date + '/' + region + '/s3/aws4_request'
        to_sign = '\n'.join(['AWS4-HMAC-SHA256', timestamp, scope, hashlib.sha256(canonical.encode()).hexdigest()])
        signing = hmac.new(('AWS4' + secret).encode(), date.encode(), hashlib.sha256).digest()
        for part in [region, 's3', 'aws4_request']:
            signing = hmac.new(signing, part.encode(), hashlib.sha256).digest()
        signature = hmac.new(signing, to_sign.encode(), hashlib.sha256).hexdigest()
        headers['Authorization'] = f'AWS4-HMAC-SHA256 Credential={access}/{scope}, SignedHeaders={names}, Signature={signature}'
        req = urllib.request.Request(self.s3_endpoint + path, method=method,
                                     data=content if method in ['PUT', 'POST'] else None, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=15) as response:
                status, raw = response.status, response.read()
        except urllib.error.HTTPError as error:
            status, raw = error.code, error.read()
        self.expect(status in expected, 'signed local fixture ' + method + ': HTTP' + str(status))
        return status, raw

    def download(self, file_response):
        self.local_url(file_response['url'])
        with urllib.request.urlopen(file_response['url'], timeout=20) as response:
            content = response.read()
            disposition = response.headers.get('Content-Disposition', '')
        self.expect(content.startswith(b'%PDF-'), 'authorized signed download returns actual PDF')
        self.expect('filename*=' in disposition, 'signed download exposes safe UTF8 filename')
        return content

    def new_upload(self, assignment, token, client=None):
        return self.api(f'/assignments/{assignment}/uploads', token, 'POST',
                        {'client_upload_id': client or str(uuid.uuid4())}, 201)

    def complete(self, upload, token):
        return self.api(f'/uploads/{upload["job"]["id"]}/complete', token, 'POST', {}, 202)

    def wait_job(self, job_id, token, status='ready', error=None, timeout=90):
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            response = self.api('/jobs/' + job_id, token, record=False)
            if response['status'] in ['ready', 'failed', 'cancelled']:
                self.expect(response['status'] == status, 'job reaches terminal state ' + status)
                if error:
                    allowed = error if isinstance(error, (list, tuple)) else [error]
                    self.expect(response['error_code'] in allowed, 'job rejection is ' + '/'.join(allowed))
                return response
            time.sleep(.3)
        raise AssertionError('job does not finish within bounded integration timeout')

    def process(self, assignment, token, content, *, status='ready', error=None):
        upload = self.new_upload(assignment, token)
        self.upload_post(upload['upload'], content)
        self.complete(upload, token)
        self.wait_job(upload['job']['id'], token, status, error)
        return upload, self.api(f'/assignments/{assignment}/workspace', token)

    def row(self, query, args=()):
        conn = mysql_connection(self.env)
        cursor = conn.cursor(dictionary=True)
        try:
            cursor.execute(query, args)
            return cursor.fetchone()
        finally:
            cursor.close()
            conn.close()

    def fixture_sql(self, query, args=()):
        conn = mysql_connection(self.env)
        cursor = conn.cursor()
        try:
            cursor.execute(query, args)
            conn.commit()
        finally:
            cursor.close()
            conn.close()

    def publish_result(self, result, *, producer='file-work-service', message_id=None):
        event = {'message_id': message_id or 'filework:' + result['job_id'] + ':' + str(result['generation']) + ':' + result['claim_token'] + ':result',
                 'message_type': 'homework.pdf.completed.v1', 'schema_version': 1,
                 'occurred_at': datetime.now(timezone.utc).isoformat(), 'producer': producer,
                 'correlation_id': 'dev-test-' + self.run, 'payload': result}
        self.rabbit('POST', '/exchanges/' + urllib.parse.quote(self.env['RABBITMQ_VHOST'], safe='') + '/cpm.events/publish', {
            'routing_key': 'homework.pdf.completed.v1', 'properties': {'delivery_mode': 2, 'content_type': 'application/json'},
            'payload': json.dumps(event), 'payload_encoding': 'string',
        })

    def rabbit(self, method, path, body=None):
        endpoint = self.env.get('RABBITMQ_MANAGEMENT_URL', 'http://127.0.0.1:15672')
        parsed = urllib.parse.urlsplit(endpoint)
        if parsed.scheme != 'http' or parsed.hostname != '127.0.0.1' or parsed.port != 15672:
            raise AssertionError('Rabbit fixture management must use fixed loopback15672')
        auth = base64.b64encode((self.env['RABBITMQ_USER'] + ':' + self.env['RABBITMQ_PASSWORD']).encode()).decode()
        req = urllib.request.Request(endpoint + '/api' + path, method=method,
                                     data=None if body is None else json.dumps(body).encode(),
                                     headers={'Authorization': 'Basic ' + auth, 'Content-Type': 'application/json'})
        with urllib.request.urlopen(req, timeout=10) as response:
            raw = response.read()
        result = json.loads(raw) if raw else {}
        if path.endswith('/publish'):
            self.expect(result.get('routed') is True, 'result fixture delivered through actual Rabbit exchange')
        return result

    def compose(self, operation, service):
        if service not in ['file-work-service', 'rabbitmq', 's3-test'] or operation not in ['pause', 'unpause']:
            raise AssertionError('chaos operation outside isolated integration fixture')
        docker = '/Applications/Docker.app/Contents/Resources/bin/docker'
        args = [docker, 'compose', '--project-directory', str(ROOT / 'dev'),
                '--env-file', str(ROOT / 'dev/.env'), '--env-file', str(ROOT / 'dev/local/homework-compose.env'),
                '-f', str(ROOT / 'dev/compose.yaml'), '-f', str(ROOT / 'dev/compose.homework.yaml'),
                '--profile', 'auth', '--profile', 'core', '--profile', 'homework',
                '--profile', 's3-test', '--profile', 'rabbit', operation, service]
        result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=45)
        self.expect(result.returncode == 0, 'local fixture ' + service + ' ' + operation)
        if operation == 'pause':
            self.paused.add(service)
        else:
            self.paused.discard(service)

    @contextmanager
    def pause(self, service):
        self.compose('pause', service)
        try:
            yield
        finally:
            self.compose('unpause', service)

    def cleanup(self):
        for service in list(self.paused):
            self.compose('unpause', service)
        super().cleanup()


def main(s):
    s.request('/health/ready')
    until = time.monotonic() + 15
    ready = False
    while time.monotonic() < until:
        try:
            ready = s.request('/readyz', origin=FILEWORK, code=(200, 503), record=False).get('status') is True
        except urllib.error.URLError:
            ready = False
        if ready:
            break
        time.sleep(.3)
    s.expect(ready, 'file-work-service Rabbit consumer becomes ready within15seconds')
    s.request('/readyz', origin=FILEWORK)
    s.admin, _ = s.login('dev_admin')
    student, actor, _ = s.fixture_student()
    other, other_actor, _ = s.fixture_student(1002)
    proctor, _ = s.login('dev_proctor_1')
    proctor2, _ = s.login('dev_proctor_2')
    viewer, editor, delegated_roles = s.fixture_staff()
    kind = s.type()
    policy = s.save_settings()
    source = pdf(image=True)
    assignment, _ = s.assignment(kind['id'])
    assignment_id = assignment['id']
    client = str(uuid.uuid4())
    s.api(f'/assignments/{assignment_id}/uploads', student, 'POST', {'client_upload_id': 'bad'}, 400)
    s.api(f'/assignments/{assignment_id}/uploads', proctor, 'POST', {'client_upload_id': client}, 403)
    s.api(f'/assignments/{assignment_id}/submit', student, 'POST', {}, 409)
    upload = s.new_upload(assignment_id, student, client)
    repeated = s.new_upload(assignment_id, student, client.upper())
    s.expect(upload['job']['id'] == repeated['job']['id'], 'client UUID retry is canonicalized and idempotent')
    s.expect(upload['upload']['method'] == 'POST', 'presigned upload uses direct browser POST')
    post_policy = json.loads(base64.b64decode(upload['upload']['fields']['policy']))
    s.expect(any(condition[0] == 'content-length-range' and condition[-1] == policy['max_source_bytes']
                 for condition in post_policy['conditions'] if isinstance(condition, list)), 'S3 policy enforces configured source byte limit')
    s.api(f'/uploads/{upload["job"]["id"]}/complete', student, 'POST', {}, 409)
    s.new_upload(assignment_id, student, client)  # still recoverable after complete-before-upload
    second, _ = s.assignment(kind['id'])
    s.api(f'/assignments/{second["id"]}/uploads', student, 'POST', {'client_upload_id': client}, 409)
    s.api(f'/assignments/{assignment_id}/uploads', student, 'POST', {'client_upload_id': str(uuid.uuid4())}, 409)
    s.api('/jobs/' + upload['job']['id'], other, code=404)
    s.api(f'/uploads/{upload["job"]["id"]}/complete', other, 'POST', {}, 403)
    s.api('/jobs/' + upload['job']['id'], s.admin, code=403)
    s.api('/jobs/active', viewer, code=403)
    active = s.api('/jobs/active', student)
    s.expect(upload['job']['id'] in {item['id'] for item in active['items']}, 'owner sees own active upload')
    # Signed POST cannot be redirected to another owner or another object.
    s.upload_post(upload['upload'], pdf(), fields_override={'x-amz-meta-account-id': str(other_actor['account_id'])}, codes=(400, 403))
    s.upload_post(upload['upload'], pdf(), fields_override={'key': 'homework/1001/1001/staging/forged.pdf'}, codes=(400, 403))
    s.upload_post(upload['upload'], source)
    # A settings update does not change limits already captured for this job.
    s.api('/settings', s.admin, 'PUT', {**policy, 'max_output_bytes': 1})
    s.complete(upload, student)
    s.complete(upload, student)
    s.wait_job(upload['job']['id'], student)
    s.api('/settings', s.admin, 'PUT', policy)
    work = s.api(f'/assignments/{assignment_id}/workspace', student)
    draft = work['submission']['draft_file']
    submission_id = work['submission']['id']
    s.expect(work['submission']['state'] == 'draft' and work['legacy_result'] is None, 'compression produces draft, without submitting or grading')
    s.expect(draft['page_count'] == 1 and draft['size_bytes'] < len(source) * .25, 'real scanned PDF compressed by more than75percent preserving page count')
    s.expect(work['permissions']['submit'] and not work['active_job'], 'submit enabled only after processed PDF is saved')
    private = s.api(f'/assignments/{assignment_id}/workspace?account_id={actor["account_id"]}', viewer)
    s.expect(not private['submission'].get('draft_file') and not private['submission'].get('has_draft')
             and not private['active_job'] and private['submission']['state'] == 'none', 'staff cannot observe private draft or processing state')
    s.api(f'/submissions/{submission_id}/file-url?draft=1', viewer, code=403)
    s.api(f'/submissions/{submission_id}/file-url', s.admin, code=403)
    s.api(f'/submissions/{submission_id}/file-url?draft=1', other, code=403)
    file_response = s.api(f'/submissions/{submission_id}/file-url?draft=1&download=1', student)
    compressed = s.download(file_response)
    s.expect(len(compressed) == draft['size_bytes'], 'download bytes match persisted file metadata')
    text = readable_pdf_text(compressed)
    s.expect(all(word in text for word in [b'CPM', b'Homework', b'readable', b'text']),
             'strong image compression retains actual selectable text in PDF content')
    s.metrics['compression'] = {'source_bytes': len(source), 'output_bytes': len(compressed),
                                'reduction_percent': round(100 * (1 - len(compressed) / len(source)), 2),
                                'page_count': draft['page_count'], 'selectable_text_preserved': True}
    row = s.row('SELECT sha256,object_key FROM hw_files WHERE id=%s', (draft['id'],))
    s.expect(hashlib.sha256(compressed).hexdigest() == row['sha256'], 'stored SHA256 matches actual compressed output')
    s.expect(b'DEV_PRIVATE_AUTHOR_REMOVE' not in compressed and b'DEV_PRIVATE_TITLE_REMOVE' not in compressed,
             'private source document metadata removed from stored output')
    s.expect(file_response['filename'].endswith('.pdf') and '\n' not in file_response['filename'], 'safe PDF name built from profile and assignment')
    today = datetime.now(ZoneInfo('Europe/Moscow')).date().isoformat()
    blocked = s.api(f'/assignments/{assignment_id}/manual', s.admin, 'POST',
                    {'account_id': actor['account_id'], 'date_pass': today, 'result': 100}, 409)
    s.expect(blocked['error'] == 'use_file_review', 'manual journal cannot bypass file draft review')
    bulk = s.api(f'/assignments/{assignment_id}/manual/bulk', proctor, 'POST', {'group_id': 1001, 'date_pass': today, 'result': 100})
    s.expect(bulk['skipped_files'] >= 1, 'bulk manual pass explicitly skips file workflow')
    # Cancelling a replacement upload keeps the existing draft.
    replacement = s.new_upload(assignment_id, student)
    s.api(f'/assignments/{assignment_id}/submit', student, 'POST', {}, 409)
    s.api(f'/assignments/{assignment_id}/draft', student, 'DELETE', code=409)
    s.api('/jobs/' + replacement['job']['id'] + '/cancel', student, 'POST', {})
    s.api('/jobs/' + replacement['job']['id'] + '/cancel', student, 'POST', {})
    s.expect(s.api(f'/assignments/{assignment_id}/workspace', student)['submission']['draft_file']['id'] == draft['id'], 'cancelling replacement preserves previous draft')
    # Parallel submit retries all return the original, single submission timestamp.
    with ThreadPoolExecutor(max_workers=6) as pool:
        submissions = list(pool.map(lambda _: s.api(f'/assignments/{assignment_id}/submit', student, 'POST', {}), range(6)))
    s.expect(len({item['submitted_at_utc'] for item in submissions}) == 1, 'concurrent submit is idempotent and preserves timestamp')
    s.api(f'/assignments/{assignment_id}/uploads', student, 'POST', {'client_upload_id': str(uuid.uuid4())}, 409)
    s.api(f'/assignments/{assignment_id}/draft', student, 'DELETE', code=409)
    s.api(f'/submissions/{submission_id}/file-url', proctor2, code=403)
    s.download(s.api(f'/submissions/{submission_id}/file-url', proctor))
    s.api(f'/submissions/{submission_id}/claim', student, 'POST', {}, 403)
    s.api(f'/submissions/{submission_id}/claim', viewer, 'POST', {}, 403)
    s.api(f'/submissions/{submission_id}/claim', proctor2, 'POST', {}, 403)
    s.api(f'/submissions/{submission_id}/claim', proctor, 'POST', {})
    s.api(f'/submissions/{submission_id}/claim', proctor, 'POST', {})
    s.api(f'/submissions/{submission_id}/takeover', proctor, 'POST', {}, 409)
    s.api(f'/submissions/{submission_id}/takeover', s.admin, 'POST', {})
    s.api(f'/submissions/{submission_id}/grade', proctor, 'POST', {'result': 80}, 409)
    s.api(f'/submissions/{submission_id}/release', s.admin, 'POST', {})
    s.api(f'/submissions/{submission_id}/release', s.admin, 'POST', {})
    s.api(f'/submissions/{submission_id}/claim', proctor, 'POST', {})
    s.api(f'/submissions/{submission_id}/request-revision', proctor, 'POST', {'message': ''}, 409)
    s.api(f'/submissions/{submission_id}/request-revision', proctor, 'POST', {'message': 'x' * 1001}, 400)
    s.api(f'/submissions/{submission_id}/request-revision', proctor, 'POST', {'message': 'DEV correct page1'})
    s.api(f'/submissions/{submission_id}/request-revision', proctor, 'POST', {'message': 'DEV correct page1'})
    revision = s.api(f'/assignments/{assignment_id}/workspace', student)
    old_current = revision['submission']['current_file']['id']
    s.expect(revision['submission']['revision_count'] == 1 and revision['submission']['state'] == 'revision_requested', 'revision request repeat increments count only once')
    _, revision = s.process(assignment_id, student, pdf(label='CPM revised readable text'))
    s.expect(revision['submission']['current_file']['id'] == old_current and revision['submission']['draft_file'], 'revision keeps submitted PDF while processing separate draft')
    s.api(f'/assignments/{assignment_id}/draft', student, 'DELETE')
    kept = s.api(f'/assignments/{assignment_id}/workspace', student)['submission']
    s.expect(kept['state'] == 'revision_requested' and kept['current_file']['id'] == old_current and not kept['draft_file'], 'removing revision draft retains current submission and revision state')
    _, revision = s.process(assignment_id, student, pdf(label='CPM final revised text'))
    revised = s.api(f'/assignments/{assignment_id}/submit', student, 'POST', {})
    s.expect(revised['submitted_at_utc'] >= submissions[0]['submitted_at_utc'], 'revision submission establishes its own submission date')
    s.api(f'/submissions/{submission_id}/claim', proctor, 'POST', {})
    for bad in [None, True, -1, 101, 80.5, '80']:
        s.api(f'/submissions/{submission_id}/grade', proctor, 'POST', {'result': bad}, 400)
    # Own synthetic timestamp deliberately crosses UTC/Moscow midnight.
    s.fixture_sql('UPDATE hw_assignments SET deadline=%s WHERE id=%s', ('2026-09-12', assignment_id))
    s.fixture_sql('UPDATE hw_submissions SET submitted_at_utc=%s WHERE id=%s AND account_id=%s', ('2026-09-12 21:30:00', submission_id, actor['account_id']))
    grade = s.api(f'/submissions/{submission_id}/grade', proctor, 'POST', {})
    s.expect(grade['result'] == 95, 'automatic grade uses Moscow submission date after UTC midnight boundary')
    s.api(f'/submissions/{submission_id}/grade', proctor, 'POST', {'result': 95})
    s.api(f'/submissions/{submission_id}/grade', proctor, 'POST', {'result': 94}, 409)
    s.api(f'/submissions/{submission_id}/edit-grade', viewer, 'POST', {'result': 90}, 403)
    s.api(f'/submissions/{submission_id}/edit-grade', proctor, 'POST', {'result': None}, 400)
    s.api(f'/submissions/{submission_id}/edit-grade', proctor, 'POST', {'result': 90})
    archived = s.api('/archive?assignment_id=' + str(assignment_id), proctor)
    s.expect(len(archived['items']) == 1 and archived['items'][0]['result'] == 90, 'archive returns graded PDF with edited grade')
    s.expect(not s.api('/review-queue?assignment_id=' + str(assignment_id), proctor)['items'], 'graded submission removed from review queue')
    s.expect(not s.api('/archive?assignment_id=' + str(assignment_id), proctor2)['items'], 'other proctor cannot list foreign group archive')
    s.core('/roles/' + str(delegated_roles[0]), s.admin, 'PUT', {
        'name': s.prefix + ' queue-only editor',
        'permissions': {'review-queue': {'view': True, 'edit': True}},
    })
    s.api('/archive', viewer, code=403)
    s.api(f'/submissions/{submission_id}/file-url', viewer, code=403)
    s.api(f'/submissions/{submission_id}/edit-grade', viewer, 'POST', {'result': 89}, 403)
    s.api(f'/submissions/{submission_id}/resubmit', viewer, 'POST', {}, 403)
    s.core('/roles/' + str(delegated_roles[0]), s.admin, 'PUT', {
        'name': s.prefix + ' archive-only editor',
        'permissions': {'homework-archive': {'view': True, 'edit': True}},
    })
    s.api('/review-queue', viewer, code=403)
    s.api(f'/submissions/{submission_id}/claim', viewer, 'POST', {}, 403)
    s.download(s.api(f'/submissions/{submission_id}/file-url', viewer))
    s.api(f'/submissions/{submission_id}/edit-grade', viewer, 'POST', {'result': 89})
    s.expect(s.api(f'/assignments/{assignment_id}/workspace?account_id={actor["account_id"]}', viewer)['legacy_result']['result'] == 89,
             'live delegated archive-only editor can edit grade without gaining queue rights')
    s.api(f'/submissions/{submission_id}/resubmit', student, 'POST', {}, 403)
    s.api(f'/submissions/{submission_id}/resubmit', proctor, 'POST', {})
    s.api(f'/submissions/{submission_id}/resubmit', proctor, 'POST', {})
    clean = s.api(f'/assignments/{assignment_id}/workspace', student)
    s.expect(clean['submission']['state'] == 'none' and not clean['submission']['has_file']
             and clean['legacy_result']['status'] == 0 and clean['legacy_result']['date_pass'] is None,
             'resubmit clears files and grade while keeping unique submission identity')
    # Real worker rejects each prohibited input through Rabbit, without creating draft.
    for label, content, error in [('bad signature', b'not-a-pdf', 'invalid_pdf_signature'),
                                  ('corrupt structure', b'%PDF-1.7\ninvalid\n', 'corrupt_pdf'),
                                  ('empty PDF', pdf(pages=0), 'empty_pdf'),
                                  ('active content', pdf(active=True), 'active_content'),
                                  ('encryption', pdf(encrypted=True), 'encrypted_pdf')]:
        doc, _ = s.assignment(kind['id'], name=s.prefix + ' ' + label)
        _, failed = s.process(doc['id'], student, content, status='failed', error=error)
        s.expect(not failed['submission']['has_file'] and not failed['submission']['has_draft']
                 and failed['legacy_result'] is None, 'rejected ' + label + ' cannot appear as submitted or graded')
    s.api('/settings', s.admin, 'PUT', {**policy, 'max_pages': 2})
    boundary, _ = s.assignment(kind['id'])
    _, boundary_work = s.process(boundary['id'], student, pdf(pages=2))
    s.expect(boundary_work['submission']['draft_file']['page_count'] == 2,
             'PDF exactly at persisted page ceiling is accepted with all pages')
    doc, _ = s.assignment(kind['id'])
    s.process(doc['id'], student, pdf(pages=3), status='failed', error='too_many_pages')
    s.api('/settings', s.admin, 'PUT', {**policy, 'max_output_bytes': 1})
    doc, _ = s.assignment(kind['id'])
    output_limited, _ = s.process(doc['id'], student, pdf(), status='failed', error='processed_too_large')
    s.api('/settings', s.admin, 'PUT', policy)
    retry_id = output_limited['job']['id']
    for attempt in range(1, 4):
        retried = s.api('/jobs/' + retry_id + '/retry', student, 'POST', {}, 202)
        s.expect(retried['manual_attempts'] == attempt, 'manual retry count increments exactly once')
        s.wait_job(retry_id, student, 'failed', 'processed_too_large')
    s.api('/jobs/' + retry_id + '/retry', student, 'POST', {}, 409)
    s.expect(s.api(f'/assignments/{doc["id"]}/workspace', student)['submission']['state'] == 'none',
             'failed retries preserve original immutable output policy and no submission')
    s.api('/settings', s.admin, 'PUT', {**policy, 'max_source_bytes': 100})
    doc, _ = s.assignment(kind['id'])
    small = s.new_upload(doc['id'], student)
    s.upload_post(small['upload'], pdf(), codes=(400, 403))
    small_row = s.row('SELECT source_key FROM hw_jobs WHERE id=%s', (small['job']['id'],))
    s.signed_s3('PUT', small_row['source_key'], pdf(), {'account-id': actor['account_id']})
    s.api('/uploads/' + small['job']['id'] + '/complete', student, 'POST', {}, 413)
    s.api('/jobs/' + small['job']['id'] + '/cancel', student, 'POST', {})
    s.api('/settings', s.admin, 'PUT', policy)
    # Complete validates actual object owner even if fixture credentials bypass POST policy.
    doc, _ = s.assignment(kind['id'])
    wrong_owner = s.new_upload(doc['id'], student)
    job_row = s.row('SELECT * FROM hw_jobs WHERE id=%s', (wrong_owner['job']['id'],))
    s.signed_s3('PUT', job_row['source_key'], pdf(), {'account-id': other_actor['account_id']})
    s.api('/uploads/' + wrong_owner['job']['id'] + '/complete', student, 'POST', {}, 403)
    s.api('/jobs/' + wrong_owner['job']['id'] + '/cancel', student, 'POST', {})
    if s.chaos:
        chaos_tests(s, kind['id'], student, actor)


def chaos_tests(s, type_id, student, actor):
    # Pause operations are opt-in because broker outages also affect sibling services.
    doc, _ = s.assignment(type_id)
    with s.pause('file-work-service'):
        upload = s.new_upload(doc['id'], student)
        s.upload_post(upload['upload'], pdf())
        s.complete(upload, student)
        row = s.row('SELECT * FROM hw_jobs WHERE id=%s', (upload['job']['id'],))
        s.signed_s3('PUT', row['source_key'], pdf(label='Changed after completion'), {'account-id': actor['account_id']})
    s.wait_job(upload['job']['id'], student, 'failed', 'source_changed')
    # Outbox persists across broker outage and delivers only after reconnect.
    doc, _ = s.assignment(type_id)
    upload = s.new_upload(doc['id'], student)
    s.upload_post(upload['upload'], pdf())
    with s.pause('rabbitmq'):
        s.complete(upload, student)
        pending = s.row("SELECT COUNT(*) n FROM hw_outbox WHERE published_at IS NULL AND JSON_UNQUOTE(JSON_EXTRACT(body_json,'$.payload.job_id'))=%s", (upload['job']['id'],))
        s.expect(pending['n'] >= 1, 'broker outage retains transactional pending outbox record')
    s.wait_job(upload['job']['id'], student, timeout=110)
    work = s.api(f'/assignments/{doc["id"]}/workspace', student)
    file_row = s.row('SELECT * FROM hw_files WHERE id=%s', (work['submission']['draft_file']['id'],))
    job_row = s.row('SELECT * FROM hw_jobs WHERE id=%s', (upload['job']['id'],))
    result = {key: job_row[key] for key in ['generation', 'organization_id', 'division_id', 'output_key']}
    result.update({'job_id': job_row['id'], 'size_bytes': file_row['size_bytes'], 'page_count': file_row['page_count'],
                   'sha256': file_row['sha256'], 'error_code': '', 'claim_token': job_row['lease_token']})
    for _ in range(3):
        s.publish_result(result)
    time.sleep(1)
    count = s.row('SELECT COUNT(*) n FROM hw_files WHERE submission_id=%s', (work['submission']['id'],))
    s.expect(count['n'] == 1, 'duplicate Rabbit result delivery creates exactly one stored file')
    s.download(s.api(f'/submissions/{work["submission"]["id"]}/file-url?draft=1', student))
    # Upload expiry uses its persisted upload timeout, not current settings.
    doc, _ = s.assignment(type_id)
    abandoned = s.new_upload(doc['id'], student)
    s.fixture_sql('UPDATE hw_jobs SET created_at=UTC_TIMESTAMP(6)-INTERVAL 2 DAY,updated_at=UTC_TIMESTAMP(6)-INTERVAL 2 DAY WHERE id=%s', (abandoned['job']['id'],))
    s.wait_job(abandoned['job']['id'], student, 'failed', 'upload_expired', timeout=30)
    # The browser may have uploaded successfully and lost its complete request.
    # Expiry must not make a recoverable source create a command with empty ETag.
    doc, _ = s.assignment(type_id)
    uploaded_expiry = s.new_upload(doc['id'], student)
    s.upload_post(uploaded_expiry['upload'], pdf(label='Uploaded without completion'))
    s.fixture_sql('UPDATE hw_jobs SET created_at=UTC_TIMESTAMP(6)-INTERVAL 2 DAY,updated_at=UTC_TIMESTAMP(6)-INTERVAL 2 DAY WHERE id=%s', (uploaded_expiry['job']['id'],))
    s.wait_job(uploaded_expiry['job']['id'], student, 'failed', 'upload_expired', timeout=30)
    s.api('/jobs/' + uploaded_expiry['job']['id'] + '/retry', student, 'POST', {}, 202)
    s.wait_job(uploaded_expiry['job']['id'], student)
    accepted = s.row('SELECT source_etag,source_size FROM hw_jobs WHERE id=%s', (uploaded_expiry['job']['id'],))
    s.expect(bool(accepted['source_etag']) and accepted['source_size'] > 0,
             'retry of uploaded expired job validates owner/size and captures source ETag before dispatch')
    # Cancellation fences a legitimately claimed worker result, even with valid metadata.
    doc, _ = s.assignment(type_id)
    with s.pause('file-work-service'):
        cancelled = s.new_upload(doc['id'], student)
        s.upload_post(cancelled['upload'], pdf())
        s.complete(cancelled, student)
        row = s.row('SELECT * FROM hw_jobs WHERE id=%s', (cancelled['job']['id'],))
        key = s.local['HOMEWORK_SERVICE_KEY']
        claim = s.request('/internal/homework/file-jobs/' + row['id'] + '/claim', method='POST',
                          body={'generation': row['generation']}, headers={'X-Service-Key': key})['data']
        s.expect(claim['run'] is True and bool(claim.get('claim_token')), 'internal worker claim creates fencing token')
        s.api('/jobs/' + row['id'] + '/cancel', student, 'POST', {})
        payload = pdf(label='Late worker result')
        digest = hashlib.sha256(payload).hexdigest()
        s.signed_s3('PUT', row['output_key'], payload, {'job_id': row['id'], 'generation': row['generation'],
                    'organization_id': row['organization_id'], 'division_id': row['division_id'], 'page_count': 1, 'sha256': digest})
        late = {'job_id': row['id'], 'generation': row['generation'], 'organization_id': row['organization_id'],
                'division_id': row['division_id'], 'output_key': row['output_key'], 'size_bytes': len(payload),
                'page_count': 1, 'sha256': digest, 'error_code': '', 'claim_token': claim['claim_token']}
        s.publish_result(late)
        time.sleep(1)
        work = s.api(f'/assignments/{doc["id"]}/workspace', student)
        s.expect(work['submission']['state'] == 'none' and not work['submission']['has_draft'], 'late success cannot restore cancelled submission')
        s.expect(s.row('SELECT status FROM hw_jobs WHERE id=%s', (row['id'],))['status'] == 'cancelled', 'late success cannot alter cancelled job')
    # Soft-deleting the assignment must also fence an already claimed worker.
    doc, _ = s.assignment(type_id)
    with s.pause('file-work-service'):
        deleted = s.new_upload(doc['id'], student)
        s.upload_post(deleted['upload'], pdf())
        s.complete(deleted, student)
        row = s.row('SELECT * FROM hw_jobs WHERE id=%s', (deleted['job']['id'],))
        claim = s.request('/internal/homework/file-jobs/' + row['id'] + '/claim', method='POST',
                          body={'generation': row['generation']}, headers={'X-Service-Key': s.local['HOMEWORK_SERVICE_KEY']})['data']
        s.api(f'/assignments/{doc["id"]}', s.admin, 'DELETE')
        payload = pdf(label='Late deleted assignment worker')
        digest = hashlib.sha256(payload).hexdigest()
        s.signed_s3('PUT', row['output_key'], payload, {'job_id': row['id'], 'generation': row['generation'],
                    'organization_id': row['organization_id'], 'division_id': row['division_id'], 'page_count': 1, 'sha256': digest})
        s.publish_result({'job_id': row['id'], 'generation': row['generation'], 'organization_id': row['organization_id'],
                          'division_id': row['division_id'], 'output_key': row['output_key'], 'size_bytes': len(payload),
                          'page_count': 1, 'sha256': digest, 'error_code': '', 'claim_token': claim['claim_token']})
        time.sleep(1)
        state = s.row('SELECT state,draft_file_id,current_file_id FROM hw_submissions WHERE id=%s', (row['submission_id'],))
        s.expect(state['state'] == 'deleted' and state['draft_file_id'] is None and state['current_file_id'] is None,
                 'late success cannot restore a deleted assignment submission or files')
        s.api('/jobs/' + row['id'], student, code=404)
        s.api(f'/assignments/{doc["id"]}/workspace', student, code=404)
    # A lost worker lease advances generation and output key before redispatch.
    doc, _ = s.assignment(type_id)
    with s.pause('file-work-service'):
        leased = s.new_upload(doc['id'], student)
        s.upload_post(leased['upload'], pdf())
        s.complete(leased, student)
        old = s.row('SELECT * FROM hw_jobs WHERE id=%s', (leased['job']['id'],))
        s.request('/internal/homework/file-jobs/' + old['id'] + '/claim', student, 'POST',
                  {'generation': old['generation']}, 403)
        s.request('/internal/homework/file-jobs/' + old['id'] + '/claim', method='POST',
                  body={'generation': old['generation']}, code=403, headers={'X-Service-Key': 'wrong-key'})
        claim = s.request('/internal/homework/file-jobs/' + old['id'] + '/claim', method='POST',
                          body={'generation': old['generation']}, headers={'X-Service-Key': s.local['HOMEWORK_SERVICE_KEY']})['data']
        s.request('/internal/homework/file-jobs/' + old['id'] + '/claim', method='POST',
                  body={'generation': old['generation']}, code=409, headers={'X-Service-Key': s.local['HOMEWORK_SERVICE_KEY']})
        s.request('/internal/homework/file-jobs/' + old['id'] + '/release', method='POST',
                  body={'generation': old['generation'], 'claim_token': str(uuid.uuid4())},
                  headers={'X-Service-Key': s.local['HOMEWORK_SERVICE_KEY']})
        s.expect(s.row('SELECT status,lease_token FROM hw_jobs WHERE id=%s', (old['id'],))['lease_token'] == claim['claim_token'],
                 'foreign release token cannot clear an active worker lease')
        s.fixture_sql('UPDATE hw_jobs SET lease_until=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE id=%s', (old['id'],))
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            recovered = s.row('SELECT * FROM hw_jobs WHERE id=%s', (old['id'],))
            if recovered['generation'] > old['generation']:
                break
            time.sleep(.3)
        s.expect(recovered['generation'] == old['generation'] + 1 and recovered['output_key'] != old['output_key'],
                 'expired worker lease advances generation and writes to separate output key')
        stale_claim = s.request('/internal/homework/file-jobs/' + old['id'] + '/claim', method='POST',
                                 body={'generation': old['generation']}, headers={'X-Service-Key': s.local['HOMEWORK_SERVICE_KEY']})['data']
        s.expect(stale_claim['run'] is False, 'old generation cannot regain worker lease after recovery')
        payload = pdf(label='Expired old worker')
        digest = hashlib.sha256(payload).hexdigest()
        s.signed_s3('PUT', old['output_key'], payload, {'job_id': old['id'], 'generation': old['generation'],
                    'organization_id': old['organization_id'], 'division_id': old['division_id'], 'page_count': 1, 'sha256': digest})
        stale = {'job_id': old['id'], 'generation': old['generation'], 'organization_id': old['organization_id'],
                 'division_id': old['division_id'], 'output_key': old['output_key'], 'size_bytes': len(payload),
                 'page_count': 1, 'sha256': digest, 'error_code': '', 'claim_token': claim['claim_token']}
        s.publish_result(stale)
        time.sleep(1)
        unchanged = s.row('SELECT * FROM hw_jobs WHERE id=%s', (old['id'],))
        no_files = s.row('SELECT COUNT(*) n FROM hw_files WHERE submission_id=%s', (old['submission_id'],))
        s.expect(unchanged['generation'] == recovered['generation'] and unchanged['status'] == 'queued' and no_files['n'] == 0,
                 'expired lease result cannot overwrite new generation or attach its old PDF')
    s.wait_job(leased['job']['id'], student)
    latest = s.api(f'/assignments/{doc["id"]}/workspace', student)
    latest_row = s.row('SELECT object_key FROM hw_files WHERE id=%s', (latest['submission']['draft_file']['id'],))
    s.expect(latest_row['object_key'] == recovered['output_key'], 'redispatched worker saves only recovered generation output')
    # Storage outage retains a retryable deletion task while business removal succeeds.
    with s.pause('s3-test'):
        s.api(f'/assignments/{doc["id"]}/draft', student, 'DELETE')
        # Prioritize only this own fixture, so another pending deletion cannot
        # consume the whole bounded maintenance pass during the outage.
        s.fixture_sql('UPDATE hw_delete_jobs SET available_at=UTC_TIMESTAMP(6)-INTERVAL 1 DAY WHERE object_key=%s', (latest_row['object_key'],))
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            deletion = s.row('SELECT * FROM hw_delete_jobs WHERE object_key=%s', (latest_row['object_key'],))
            if deletion and deletion['error_code'] == 'storage_unavailable':
                break
            time.sleep(.3)
        s.expect(deletion and deletion['status'] == 'queued' and deletion['attempts'] > 0
                 and deletion['error_code'] == 'storage_unavailable',
                 'S3 outage retains attempted deletion task for retry')
    # Bring the synthetic task due now instead of waiting its normal30second backoff.
    s.fixture_sql('UPDATE hw_delete_jobs SET available_at=UTC_TIMESTAMP(6) WHERE id=%s AND status=%s', (deletion['id'], 'queued'))
    deadline = time.monotonic() + 40
    while time.monotonic() < deadline:
        deletion = s.row('SELECT * FROM hw_delete_jobs WHERE id=%s', (deletion['id'],))
        if deletion['status'] == 'done':
            break
        time.sleep(.3)
    s.expect(deletion['status'] == 'done', 'S3 object deletion recovers after storage becomes available')
    s.signed_s3('HEAD', latest_row['object_key'], expected=(404,))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('--chaos', action='store_true', help='serial fixture pauses for Rabbit/S3/worker recovery scenarios')
    args = parser.parse_args()
    suite = WorkflowSuite(args.chaos)
    error = None
    try:
        main(suite)
    except Exception as exc:
        if isinstance(exc, AssertionError):
            error = str(exc)
        elif isinstance(exc, urllib.error.URLError):
            error = 'URLError: ' + type(exc.reason).__name__
        else:
            error = type(exc).__name__
    finally:
        try:
            suite.cleanup()
        except Exception as exc:
            error = error or ('cleanup failed: ' + type(exc).__name__)
    suite.report('homework-workflow-report.json', 'failed' if error else 'passed', error)
    if error:
        raise SystemExit(error)
