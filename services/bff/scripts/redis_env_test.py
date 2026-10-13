import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest.mock import patch

from redis_env import prepare


class RedisEnvTest(unittest.TestCase):
    def test_private_and_idempotent(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "local" / "bff-redis.env"
            prepare(path)
            original = path.read_bytes()
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            os.chmod(path, 0o644)
            prepare(path)
            self.assertEqual(path.read_bytes(), original)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_invalid_file_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "bff-redis.env"
            path.write_text("BFF_REDIS_PASSWORD=invalid\n")
            original = path.read_bytes()
            with self.assertRaises(ValueError):
                prepare(path)
            self.assertEqual(path.read_bytes(), original)

    def test_symlink_target_preserved(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "outside.env"
            target.write_text("BFF_REDIS_PASSWORD=" + "a" * 64 + "\n")
            target.chmod(0o644)
            link = Path(directory) / "bff-redis.env"
            link.symlink_to(target)
            with self.assertRaises(ValueError):
                prepare(link)
            self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o644)
            self.assertTrue(link.is_symlink())

    def test_nonregular_and_foreign_owner_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "bff-redis.env"
            path.mkdir()
            with self.assertRaises(ValueError):
                prepare(path)
            path.rmdir()
            path.write_text("BFF_REDIS_PASSWORD=" + "b" * 64 + "\n")
            path.chmod(0o644)
            original = path.read_bytes()
            with patch("redis_env.os.geteuid", return_value=path.stat().st_uid + 1):
                with self.assertRaises(ValueError):
                    prepare(path)
            self.assertEqual(path.read_bytes(), original)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o644)


if __name__ == "__main__":
    unittest.main()
