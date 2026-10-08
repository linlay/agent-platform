#!/usr/bin/env python3
"""Preserve a locked archive when its rebuilt payload is unchanged."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import stat
import tarfile
import tempfile
import zipfile


def entry_name(name):
    path = PurePosixPath(name.rstrip('/'))
    if not path.parts or path.is_absolute() or '..' in path.parts or '\\' in name:
        raise ValueError(f'unsafe archive entry: {name}')
    return path.as_posix()


def payload(path, archive_format, windows=False):
    entries = {}

    def add(name, kind, data=b'', mode=0):
        name = entry_name(name)
        if name in entries:
            raise ValueError(f'duplicate archive entry: {name}')
        executable = False if windows else bool(mode & 0o111)
        entries[name] = (kind, hashlib.sha256(data).digest(), executable if kind == 'file' else False)

    if archive_format == 'tar.gz':
        with tarfile.open(path, 'r:gz') as archive:
            for item in archive:
                if item.isfile():
                    with archive.extractfile(item) as source:
                        add(item.name, 'file', source.read(), item.mode)
                elif item.isdir():
                    add(item.name, 'directory')
                elif item.issym():
                    add(item.name, 'symlink', item.linkname.encode('utf-8'))
                else:
                    raise ValueError(f'unsupported archive entry: {item.name}')
    elif archive_format == 'zip':
        with zipfile.ZipFile(path) as archive:
            for item in archive.infolist():
                mode = item.external_attr >> 16
                if item.is_dir():
                    add(item.filename, 'directory')
                elif stat.S_ISLNK(mode):
                    add(item.filename, 'symlink', archive.read(item))
                else:
                    add(item.filename, 'file', archive.read(item), mode)
    else:
        raise ValueError(f'unsupported archive format: {archive_format}')
    return entries


def reuse_archive(reference, rebuilt, expected_sha256, archive_format, windows=False):
    if reference.is_symlink() or rebuilt.is_symlink():
        raise ValueError('release archives must be regular files')
    reference_bytes = reference.read_bytes()
    if hashlib.sha256(reference_bytes).hexdigest() != expected_sha256.lower():
        raise ValueError('canonical release archive SHA-256 does not match the lock')
    if payload(reference, archive_format, windows) != payload(rebuilt, archive_format, windows):
        return False
    if reference.resolve() != rebuilt.resolve():
        fd, name = tempfile.mkstemp(prefix='.locked-archive-', dir=rebuilt.parent)
        try:
            with os.fdopen(fd, 'wb') as destination:
                destination.write(reference_bytes)
            os.replace(name, rebuilt)
        finally:
            if os.path.exists(name):
                os.unlink(name)
    rebuilt.with_name(rebuilt.name + '.sha256').write_text(
        f'{expected_sha256.lower()}  {rebuilt.name}\n', encoding='utf-8')
    return True


def within(root, relative):
    path = root / entry_name(relative)
    path.resolve().relative_to(root.resolve())
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--lock', type=Path, required=True)
    parser.add_argument('--source-root', type=Path, required=True)
    parser.add_argument('--built-root', type=Path, required=True)
    parser.add_argument('--component', required=True)
    parser.add_argument('--target', required=True)
    args = parser.parse_args()
    lock = json.loads(args.lock.read_text(encoding='utf-8'))
    component = next(item for item in lock['components'] if item['name'] == args.component)
    if component['kind'] != 'archive-tree':
        raise ValueError('locked payload reuse requires an archive-tree component')
    target = component['targets'][args.target.replace('/', '-')]
    repository = within(args.built_root, component['repository'])
    if repository.joinpath('VERSION').read_text().strip() != component['version']:
        return  # A new local version follows the normal promotion workflow.
    if (target.get('version') or component['version']) != component['version']:
        return
    reference = within(within(args.source_root, component['repository']), target['path'])
    if not reference.exists():
        return  # A missing stable archive can still be generated and promoted.
    rebuilt = within(repository, target['path'])
    if reuse_archive(reference, rebuilt, target['sha256'], target['format'], args.target.startswith('windows/')):
        print(f"[builtins-sync] {args.component} {args.target}: rebuilt payload verified; reused locked archive")


if __name__ == '__main__':
    main()
