import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("retention", Path(__file__).with_name("backup-retention.py"))
retention = importlib.util.module_from_spec(spec)
spec.loader.exec_module(retention)


class RetentionTests(unittest.TestCase):
    def test_preserves_latest_recent_and_unrecognized_directories(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            backups = []
            for day in range(1, 6):
                backup = root / f"qiyun-2026090{day}-000000-123"
                backup.mkdir()
                (backup / "manifest.json").write_text(json.dumps({"formatVersion": 2}))
                (backup / "SHA256SUMS").write_text("fixture")
                backups.append(backup)
            (root / "latest").write_text(str(backups[0]))
            (root / "manual-backup").mkdir()
            (root / "qiyun-20260801-000000-456").mkdir()
            removed = retention.prune(root, 2)
            self.assertEqual(set(removed), {backups[1].name, backups[2].name})
            for path in [backups[0], backups[3], backups[4], root / "manual-backup", root / "qiyun-20260801-000000-456"]:
                self.assertTrue(path.is_dir())

    def test_rejects_external_latest(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "backups"
            root.mkdir()
            (root / "latest").write_text(temporary)
            with self.assertRaises(ValueError):
                retention.prune(root, 7)


if __name__ == "__main__":
    unittest.main()
