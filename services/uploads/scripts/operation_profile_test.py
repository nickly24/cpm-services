"""Hermetic checks; no real credentials, Docker, SQL or HTTP reads."""
import importlib.util
import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('uploads_profile', Path(__file__).with_name('run_operation_profile.py'))
profile = importlib.util.module_from_spec(spec)
spec.loader.exec_module(profile)


class ProfileGuardTests(unittest.TestCase):
    def check_case(self, change=None, mode=0o600, symlink=False, expected=True):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'credentials.env'
            values = {name: chr(97+i)*32 for i, name in enumerate(profile.KEY_NAMES)}
            if change:
                change(values)
            path.write_text(''.join(f'{name}={value}\n' for name, value in values.items()))
            path.chmod(mode)
            if symlink:
                link = path.with_name('link.env')
                link.symlink_to(path)
                path = link
            def read_values(p):
                if p == profile.ROOT / 'dev/.env':
                    return {'AUTH_INTERNAL_KEY': 'old-internal-key'*3}
                return {k: v for line in p.read_text().splitlines() for k, _, v in [line.partition('=')]}
            with patch.object(profile, 'read_values', read_values), patch.dict(os.environ, {}, clear=True):
                if expected:
                    result = profile.operation_credentials(path)
                    self.assertEqual(set(result), set(profile.UPLOADS_KEYS))
                    self.assertEqual(len(result), 5)
                else:
                    with self.assertRaises((ValueError, OSError)):
                        profile.operation_credentials(path)

    def test_only_uploads_audiences_exported(self): self.check_case()
    def test_world_readable_rejected(self): self.check_case(mode=0o644, expected=False)
    def test_symlink_rejected(self): self.check_case(symlink=True, expected=False)
    def test_incomplete_rejected(self): self.check_case(lambda v: v.pop(profile.KEY_NAMES[2]), expected=False)
    def test_reused_audience_rejected(self): self.check_case(lambda v: v.update({profile.KEY_NAMES[6]: v[profile.KEY_NAMES[0]]}), expected=False)
    def test_old_internal_key_rejected(self): self.check_case(lambda v: v.update({profile.KEY_NAMES[0]: 'old-internal-key'*3}), expected=False)
    def test_dependency_alias_mismatch_rejected(self): self.check_case(lambda v: v.update(CORE_CARDS_OPERATION_KEY='z'*32), expected=False)
    def test_duplicate_field_rejected(self): self.check_case(lambda v: v.update(INJECTED='x\n'+profile.KEY_NAMES[0]+'=z'*32), expected=False)


if __name__ == '__main__': unittest.main()
