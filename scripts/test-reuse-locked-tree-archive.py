#!/usr/bin/env python3
import hashlib
import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location('reuse', Path(__file__).with_name('reuse-locked-tree-archive.py'))
reuse = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reuse)


def archive(path, text=b'launcher', mode=0o755, metadata=0, name='runtime/bin/pdftotext'):
    with tarfile.open(path, 'w:gz') as output:
        item = tarfile.TarInfo(name)
        item.size, item.mode, item.uid, item.mtime = len(text), mode, metadata, metadata
        output.addfile(item, io.BytesIO(text))


class ReuseTests(unittest.TestCase):
    def test_windows_zip_header_changes_reuse_locked_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            old, new = Path(directory) / 'old.zip', Path(directory) / 'new.zip'
            for path, year, mode in ((old, 2026, 0), (new, 1980, 0o755)):
                with zipfile.ZipFile(path, 'w') as output:
                    item = zipfile.ZipInfo('runtime/bin/pdftotext.exe', (year, 1, 1, 0, 0, 0))
                    item.external_attr = mode << 16
                    output.writestr(item, b'launcher')
            original = old.read_bytes()
            self.assertTrue(reuse.reuse_archive(old, new, hashlib.sha256(original).hexdigest(), 'zip', windows=True))
            self.assertEqual(original, new.read_bytes())

    def test_header_changes_reuse_the_exact_locked_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            old, new = Path(directory) / 'old.tar.gz', Path(directory) / 'new.tar.gz'
            archive(old, mode=0o555, metadata=501)
            archive(new, mode=0o755, metadata=0)
            original = old.read_bytes()
            self.assertNotEqual(original, new.read_bytes())
            sha = hashlib.sha256(original).hexdigest()
            self.assertTrue(reuse.reuse_archive(old, new, sha, 'tar.gz'))
            self.assertEqual(original, old.read_bytes())
            self.assertEqual(original, new.read_bytes())
            self.assertEqual(f'{sha}  new.tar.gz\n', new.with_name('new.tar.gz.sha256').read_text())

    def test_content_and_executable_changes_remain_conflicts(self):
        for text, mode in ((b'changed', 0o755), (b'launcher', 0o644)):
            with self.subTest(text=text, mode=mode), tempfile.TemporaryDirectory() as directory:
                old, new = Path(directory) / 'old.tar.gz', Path(directory) / 'new.tar.gz'
                archive(old)
                archive(new, text=text, mode=mode)
                original, rebuilt = old.read_bytes(), new.read_bytes()
                self.assertFalse(reuse.reuse_archive(old, new, hashlib.sha256(original).hexdigest(), 'tar.gz'))
                self.assertEqual(original, old.read_bytes())
                self.assertEqual(rebuilt, new.read_bytes())

    def test_wrong_locked_hash_and_unsafe_paths_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            old, new = Path(directory) / 'old.tar.gz', Path(directory) / 'new.tar.gz'
            archive(old)
            archive(new)
            with self.assertRaisesRegex(ValueError, 'SHA-256'):
                reuse.reuse_archive(old, new, '0' * 64, 'tar.gz')
            archive(new, name='../escape')
            with self.assertRaisesRegex(ValueError, 'unsafe archive entry'):
                reuse.payload(new, 'tar.gz')


if __name__ == '__main__':
    unittest.main()
