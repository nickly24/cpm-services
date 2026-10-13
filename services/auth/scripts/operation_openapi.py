"""Internal grant contract. Never exposed through the public BFF proxy."""
def extend_operations(document):
    def ref(name):
        return {'$ref': '#/components/schemas/' + name}
    def obj(properties):
        return {'type': 'object', 'additionalProperties': False,
                'required': list(properties), 'properties': properties}
    def nullable(schema):
        return {'anyOf': [schema, {'type': 'null'}]}
    decimal = {'type': 'string', 'pattern': '^[1-9][0-9]*$',
               'description': 'Canonical positive decimal, at most signed int64.'}
    sha = {'type': 'string', 'pattern': '^[0-9a-f]{64}$'}
    hex32 = {'type': 'string', 'pattern': '^[0-9a-f]{32}$'}
    uuid = {'type': 'string', 'format': 'uuid',
            'pattern': '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'}
    utc = {'type': 'string', 'format': 'date-time', 'pattern': 'Z$'}
    target = {'oneOf': [
        obj({'kind': {'const': 'division_users'}, 'division_id': decimal}),
        obj({'kind': {'const': 'existing_theme'}, 'direction_id': decimal, 'theme_id': decimal}),
        obj({'kind': {'const': 'create_theme'}, 'direction_id': decimal,
             'name': {'type': 'string', 'minLength': 1, 'maxLength': 2048},
             'collision_policy': {'const': 'reuse_existing'}}),
        obj({'kind': {'const': 'create_tests'}, 'direction_id': decimal}),
        obj({'kind': {'const': 'external_test'}, 'test_id': {'type': 'string', 'pattern': '^[0-9a-f]{24}$'}}),
        obj({'kind': {'const': 'exam'}, 'exam_id': decimal})]}
    schemas = document['components']['schemas']
    schemas['OperationProof'] = obj({'scheme': {'enum': [
        'core.credentials.snapshot.v1', 'core.legacy.snapshot.v1', 'cards.xlsx.v1',
        'tests.json.v1', 'tests.external-results.v1', 'exams.import.v1']},
        'validation_sha256': sha, 'input_sha256': nullable(sha), 'mapping_sha256': nullable(sha)})
    schemas['OperationBinding'] = obj({
        'contract_version': {'const': 1}, 'upload_id': hex32, 'upload_session_id': hex32,
        'job_id': hex32, 'coordinator_command_key': {'type': 'string', 'pattern': '^[a-zA-Z0-9:_-]{8,128}$'},
        'type': {'enum': ['users.credentials', 'users.legacy', 'cards.xlsx', 'tests.json',
                          'tests.external-results', 'exams.questions', 'exams.assignments', 'exams.outside']},
        'owner': {'enum': ['core', 'cards', 'tests', 'exams']}, 'native_command_key': uuid,
        'owner_session_id': nullable({'type': 'string'}), 'upload_preview_revision': decimal,
        'owner_preview_revision': nullable(decimal), 'target_revision': nullable(decimal),
        'target': target, 'source_sha256': sha, 'owner_proof': ref('OperationProof'),
        'owner_request_sha256': sha, 'apply_wire_body_sha256': sha,
        'preview_wire_body_sha256': nullable(sha), 'upload_expires_at': utc,
        'owner_preview_expires_at': nullable(utc),
        'required_division_ids': {'type': 'array', 'minItems': 1, 'maxItems': 1000,
                                  'uniqueItems': True, 'items': decimal}})
    schemas['OperationBinding']['description'] = (
        'Exact Rev2 discriminated type/target/proof/nullable metadata rules in docs/import-operation-grants-v1.md. '
        'Null fields are required. Scope is derived from the live initiator, never submitted. '
        'Division IDs must be numerically sorted and include origin. Duplicate, escaped-duplicate, '
        'unknown and case-aliased JSON fields are rejected.')
    schemas['OperationAuthorize'] = obj({
        'contract_version': {'const': 1}, 'job_id': hex32,
        'stage': {'enum': ['apply', 'preview', 'receipt', 'core-direction']},
        'method': {'enum': ['GET', 'POST']}, 'path': {'type': 'string'},
        'raw_query': {'type': 'string'}, 'force_query': {'const': False},
        'wire_body_sha256': sha, 'native_command_key': uuid})
    schemas['OperationActor'] = obj({
        'id': {'type': 'integer', 'minimum': 1}, 'account_id': {'type': 'integer', 'minimum': 1},
        'role': {'type': 'string'}, 'organization_id': {'type': 'integer', 'minimum': 1},
        'division_id': {'type': 'integer', 'minimum': 1},
        'division_ids': {'type': 'array', 'items': {'type': 'integer', 'minimum': 1}},
        'organization_admin': {'type': 'boolean'}, 'permissions': ref('PermissionMap')})
    schemas['OperationResponse'] = obj({
        'status': {'const': True}, 'contract_version': {'const': 1}, 'grant_id': hex32,
        'mode': {'enum': ['apply', 'receipt']}, 'expires_at': utc, 'actor': ref('OperationActor'),
        'division_permissions': {'type': 'array', 'items': obj({
            'division_id': {'type': 'integer', 'minimum': 1}, 'permissions': ref('PermissionMap')})},
        'binding': ref('OperationBinding')})
    schemas['OperationEmpty'] = obj({})
    schemes = document['components']['securitySchemes']
    schemes['operationServiceKey'] = {'type': 'apiKey', 'in': 'header', 'name': 'X-Service-Key',
        'description': 'Distinct issuer or owner audience key; AUTH_INTERNAL_KEY is never accepted.'}
    for suffix, body, response, issuer, issuance in [
        ('', 'OperationBinding', 'OperationResponse', True, True),
        ('/{grant}/authorize', 'OperationAuthorize', 'OperationResponse', False, False),
        ('/{grant}/recover', 'OperationEmpty', 'OperationResponse', True, True),
        ('/{grant}/revoke', 'OperationEmpty', 'Status', True, False)]:
        parameters = []
        if suffix:
            parameters.append({'name': 'grant', 'in': 'path', 'required': True, 'schema': hex32})
        if issuance:
            parameters.append({'name': 'Idempotency-Key', 'in': 'header', 'required': True, 'schema': uuid})
        responses = {'200': {'description': 'Success', 'content': {'application/json': {'schema': ref(response)}}}}
        for status in [400, 401, 403, 404, 409, 410, 413, 415, 503]:
            responses[str(status)] = {'description': 'Fail closed; unavailable or missing receipt is not proof of no apply.',
                'content': {'application/json': {'schema': ref('Error')}}}
        security = {'operationServiceKey': []}
        if issuer:
            security['bearerAuth'] = []
        document['paths']['/internal/auth/operation-grants' + suffix] = {'post': {
            'summary': 'Import operation grant ' + (suffix.rsplit('/', 1)[-1] if suffix else 'issue'),
            'description': 'Disabled by default. No query or encoded path. '
                + ('Requires current creator bearer and issuer key. Recovery is receipt-only; revoke never rolls back data. '
                   'Recover/revoke body must be exact two bytes {}.' if issuer else
                   'Owner key only; bearer and native carrier forbidden. Actual native method/path/query/body hash and '
                   'original command key must match the immutable binding. Current account, epoch and rights checked each call.'),
            'security': [security], 'parameters': parameters, 'responses': responses,
            'requestBody': {'required': True, 'content': {'application/json': {'schema': ref(body)}}}}}
