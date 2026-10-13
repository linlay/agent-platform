#!/usr/bin/env python3
"""Build an isolated KBX checkout into Platform's checksum-verified archive layout."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import platform
import struct
import subprocess
import sys
import tempfile

TARGETS = {
    'darwin/arm64': ('aarch64-apple-darwin', 'macos-arm64'),
    'darwin/amd64': ('x86_64-apple-darwin', 'macos-x64'),
    'windows/amd64': ('x86_64-pc-windows-msvc', 'windows-x64'),
    'linux/amd64': ('x86_64-unknown-linux-gnu', 'linux-x64'),
}


def validate_linux_binary(data):
    """KBX's own release tool has no Linux target; check the ELF header here."""
    if data[:4] != b'\x7fELF':
        raise ValueError('Expected an ELF executable.')
    if len(data) < 20 or data[4] != 2 or data[5] != 1:
        raise ValueError('Expected a 64-bit little-endian ELF executable.')
    kind, machine = struct.unpack_from('<HH', data, 16)
    if kind not in (2, 3) or machine != 0x3e:
        raise ValueError('Executable architecture does not match linux-x64.')


def run(args):
    if args.target not in TARGETS:
        raise ValueError('KBX release currently supports darwin/arm64, darwin/amd64, windows/amd64 and linux/amd64; unsupported target: ' + args.target)
    source = Path(args.source).resolve()
    target_dir = Path(args.target_dir).resolve()
    triple, release_key = TARGETS[args.target]
    spec = importlib.util.spec_from_file_location('kbx_release', source / 'scripts/release.py')
    release = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(release)
    env = os.environ.copy()
    env['CARGO_TARGET_DIR'] = str(target_dir)
    if args.target.startswith('darwin/'):
        if sys.platform != 'darwin':
            raise ValueError('KBX macOS artifacts must be built on macOS')
        env['MACOSX_DEPLOYMENT_TARGET'] = '12.0'
    if args.target == 'linux/amd64' and (sys.platform != 'linux' or platform.machine() not in ('x86_64', 'AMD64')):
        # glibc-linked; built in the bookworm builder so it matches the runtime image.
        raise ValueError('KBX Linux artifacts must be built on a native linux/amd64 host')
    command = ['cargo', '+stable']
    if args.target == 'windows/amd64' and sys.platform != 'win32':
        if sys.platform != 'darwin':
            raise ValueError('KBX Windows cross-build requires macOS cargo-xwin or native Windows')
        llvm = subprocess.check_output(['brew', '--prefix', 'llvm'], text=True).strip()
        env['PATH'] = str(Path(llvm) / 'bin') + os.pathsep + env.get('PATH', '')
        command.append('xwin')
    command += ['build', '--release', '--locked', '--bin', 'kbx', '--target', triple, '-j', str(args.jobs)]
    subprocess.run(command, cwd=source, env=env, check=True)
    metadata = json.loads(subprocess.check_output(['cargo', '+stable', 'metadata', '--locked', '--format-version', '1', '--filter-platform', triple], cwd=source, env=env))
    package = next(p for p in metadata['packages'] if p['name'] == 'kbx' and Path(p['manifest_path']).resolve() == source / 'Cargo.toml')
    binary = 'kbx.exe' if args.target.startswith('windows/') else 'kbx'
    data = (target_dir / triple / 'release' / binary).read_bytes()
    if release_key == 'linux-x64':
        validate_linux_binary(data)
    else:
        release.validate_binary(data, release_key)
    native = release_key == 'linux-x64' or (sys.platform == 'win32' and release_key == 'windows-x64') or (sys.platform == 'darwin' and release_key == ('macos-arm64' if release.platform.machine() == 'arm64' else 'macos-x64'))
    if native:
        subprocess.run([str(target_dir / triple / 'release' / binary), 'search', '--filter-help'], check=True, stdout=subprocess.DEVNULL)
    notices = ['Third-party dependency license inventory (Cargo metadata).', '']
    for p in sorted(metadata['packages'], key=lambda p: (p['name'], p['version'])):
        if p['id'] == package['id']:
            continue
        notices.append('{} {}\nLicense: {}\nSource: {}\n'.format(p['name'], p['version'], p.get('license') or 'See upstream license file', p.get('repository') or p.get('source') or ''))
    files = {binary: data, 'LICENSE': (source / 'LICENSE').read_bytes(), 'THIRD-PARTY-LICENSES.txt': '\n'.join(notices).encode()}
    version = 'v' + package['version'].lstrip('v')
    extension = 'zip' if args.target.startswith('windows/') else 'tar.gz'
    destination = source / 'dist' / version / ('kbx_{}_{}.{}'.format(version, args.target.replace('/', '_'), extension))
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=destination.parent) as temporary:
        archive = Path(temporary) / destination.name
        release.write_archive(archive, files)
        archive.replace(destination)
    print(destination, flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', required=True)
    parser.add_argument('--target', required=True)
    parser.add_argument('--target-dir', required=True)
    parser.add_argument('--jobs', type=int, default=2)
    args = parser.parse_args()
    try:
        if args.jobs < 1:
            raise ValueError('--jobs must be positive')
        run(args)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, 'build KBX: {}\n'.format(error))
