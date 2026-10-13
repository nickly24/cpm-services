"""Pure regression checks for dedicated stable dev worker credentials."""
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / 'dev/scripts'))
from homework_env import worker_key


class WorkerKeyTests(unittest.TestCase):
    def test_first_setup_generates_separate_key(self):
        core = 'c' * 32
        key = worker_key({'AUTH_INTERNAL_KEY': core}, {})
        self.assertGreaterEqual(len(key), 32)
        self.assertNotEqual(key, core)

    def test_existing_dedicated_key_is_stable(self):
        key = 'w' * 32
        self.assertEqual(worker_key({'AUTH_INTERNAL_KEY': 'c' * 32}, {'HOMEWORK_SERVICE_KEY': key}), key)

    def test_legacy_shared_key_is_replaced(self):
        core = 'c' * 32
        key = worker_key({'AUTH_INTERNAL_KEY': core}, {'HOMEWORK_SERVICE_KEY': core})
        self.assertNotEqual(key, core)
        self.assertEqual(worker_key({'AUTH_INTERNAL_KEY': core}, {'HOMEWORK_SERVICE_KEY': key}), key)

    def test_explicit_separate_key_is_honored(self):
        self.assertEqual(worker_key({'AUTH_INTERNAL_KEY': 'c' * 32, 'HOMEWORK_SERVICE_KEY': 'w' * 32}, {}), 'w' * 32)

    def test_explicit_shared_or_short_key_rejected(self):
        for key in ('short', 'c' * 32):
            with self.subTest(key_length=len(key)), self.assertRaises(SystemExit):
                worker_key({'AUTH_INTERNAL_KEY': 'c' * 32, 'HOMEWORK_SERVICE_KEY': key}, {})

    def test_missing_or_short_core_key_rejected(self):
        for key in ('', 'short'):
            with self.subTest(key_length=len(key)), self.assertRaises(SystemExit):
                worker_key({'AUTH_INTERNAL_KEY': key}, {'HOMEWORK_SERVICE_KEY': 'w' * 32})


if __name__ == '__main__':
    unittest.main()
