#!/usr/bin/env python3
"""Build the Apple Silicon application. Outputs must remain outside the checkout."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import re
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
NODE_VERSION = '22.23.2'
NODE_SHA = '61130f394c1630d211dd50aecc4353d379480f36d3ac913cd85dbba1aed585c6'
NPM_VERSION = '11.19.0'
NPM_SRI = 'SDd/hHg3KqHE5Ht2NHWxNYNtqCQ2pXAPLl6OtQhPyED5PHsRfrOtO199MZTIG2cQoQ1ZRI9t28shrD+2cr3AAw=='


def run(*args, **kwargs):
    subprocess.run([str(a) for a in args], check=True, **kwargs)


def download(url, destination, expected, algorithm='sha256'):
    if not destination.exists():
        pending = destination.with_suffix('.download')
        request = urllib.request.Request(url, headers={'User-Agent': 'chora-package-builder'})
        with urllib.request.urlopen(request, timeout=90) as response, pending.open('wb') as output:
            if not response.url.startswith('https://'):
                raise ValueError('insecure runtime download redirect')
            shutil.copyfileobj(response, output)
        pending.replace(destination)
    digest = hashlib.new(algorithm, destination.read_bytes()).digest()
    actual = digest.hex() if algorithm == 'sha256' else base64.b64encode(digest).decode()
    if actual != expected:
        raise ValueError('pinned runtime checksum mismatch: ' + destination.name)


def extract(archive, destination):
    # Frozen upstream archives; still reject absolute paths and escaping links.
    with tarfile.open(archive) as contents:
        for entry in contents.getmembers():
            path = Path(entry.name)
            if path.is_absolute() or '..' in path.parts or entry.isdev() or entry.isfifo():
                raise ValueError('unsafe runtime archive member')
            if entry.issym() or entry.islnk():
                target = (destination / path.parent / entry.linkname).resolve()
                if not target.is_relative_to(destination.resolve()):
                    raise ValueError('escaping runtime archive link')
        contents.extractall(destination, filter='data')


def runtime_manifest(runtime):
    result = {}
    for file in sorted(runtime.rglob('*')):
        if file.is_symlink():
            if not file.resolve().is_relative_to(runtime.resolve()):
                raise ValueError('runtime symlink leaves bundle')
        elif file.is_file():
            result[file.relative_to(runtime).as_posix()] = hashlib.sha256(file.read_bytes()).hexdigest()
    return (json.dumps(result, sort_keys=True, separators=(',', ':')) + '\n').encode()


def build(args):
    if platform.system() != 'Darwin' or platform.machine() != 'arm64':
        raise ValueError('packaging requires Apple Silicon macOS')
    output = args.output.resolve()
    if output.is_relative_to(ROOT) or ROOT.is_relative_to(output):
        raise ValueError('choose a dedicated output directory outside the source checkout')
    output.mkdir(parents=True, exist_ok=True)
    app = output / 'Chora.app'
    if app.exists():
        raise ValueError('output already contains Chora.app; choose a fresh directory')
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?', args.version):
        raise ValueError('version must be a semantic release version')
    source = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip()
    dirty = bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT))
    if args.identity and dirty:
        raise ValueError('distribution signing requires a clean committed source tree')
    cache = args.cache.resolve()
    cache.mkdir(parents=True, exist_ok=True)
    archive = cache / f'node-v{NODE_VERSION}-darwin-arm64.tar.gz'
    download(f'https://nodejs.org/dist/v{NODE_VERSION}/{archive.name}', archive, NODE_SHA)
    npm_archive = cache / f'npm-{NPM_VERSION}.tgz'
    download(f'https://registry.npmjs.org/npm/-/{npm_archive.name}', npm_archive, NPM_SRI, 'sha512')
    resources = app / 'Contents/Resources'
    runtime = resources / 'runtime'
    binaries = resources / 'bin'
    workbench = resources / 'workbench'
    for directory in [binaries, workbench, app / 'Contents/MacOS']:
        directory.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='chora-runtime-') as tmp:
        stage = Path(tmp)
        extract(archive, stage)
        shutil.move(stage / f'node-v{NODE_VERSION}-darwin-arm64', runtime)
        shutil.rmtree(runtime / 'include')
        shutil.rmtree(runtime / 'share')
        shutil.rmtree(runtime / 'lib/node_modules/npm')
        extract(npm_archive, stage)
        shutil.move(stage / 'package', runtime / 'lib/node_modules/npm')
    environment = os.environ.copy()
    environment['PATH'] = str(runtime / 'bin') + os.pathsep + environment.get('PATH', '')
    consumer = runtime / 'pi'
    consumer.mkdir()
    for filename in ['package.json', 'package-lock.json']:
        shutil.copy2(ROOT / 'internal/piinstall/assets' / filename, consumer / filename)
    lock = json.loads((consumer / 'package-lock.json').read_text())
    pi_entry = lock['packages']['node_modules/@earendil-works/pi-coding-agent']
    pi_archive = runtime / 'qualified-package.tgz'
    pi_sri = pi_entry['integrity'].removeprefix('sha512-')
    download('https://registry.npmjs.org/@earendil-works/pi-coding-agent/-/pi-coding-agent-' + pi_entry['version'] + '.tgz', pi_archive, pi_sri, 'sha512')
    # An empty explicit npm configuration prevents private registries or credentials
    # from entering the distributable closure. Lockfile integrity pins every package.
    config = output / 'empty.npmrc'
    config.write_text('')
    global_config = output / 'global.npmrc'
    global_config.write_text('')
    run(runtime / 'bin/npm', 'ci', '--prefix', consumer, '--ignore-scripts', '--omit=dev', '--no-audit', '--no-fund', '--registry=https://registry.npmjs.org', '--userconfig', config, '--globalconfig', global_config, env=environment)
    (runtime / 'bin/pi').symlink_to('../pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js')
    run(runtime / 'bin/node', '--version', env=environment)
    run(runtime / 'bin/npm', '--version', env=environment)
    run(runtime / 'bin/pi', '--version', env=environment)
    # Installed machines use these built assets; no source checkout or Go required.
    run('npm', 'run', 'web:build', cwd=ROOT)
    shutil.copytree(ROOT / 'web/dist', workbench / 'web/dist')
    observer = workbench / 'internal/agent/pi/resource_check_observer.mjs'
    observer.parent.mkdir(parents=True)
    shutil.copy2(ROOT / 'internal/agent/pi/resource_check_observer.mjs', observer)
    flags = '-s -w -X github.com/Yangyang96/chora/internal/buildinfo.Version=' + args.version
    run('go', 'build', '-trimpath', '-ldflags', flags, '-o', binaries / 'chora', './cmd/chora', cwd=ROOT)
    linux = dict(os.environ, GOOS='linux', GOARCH='arm64', CGO_ENABLED='0')
    helper = workbench / 'isolated-helper'
    run('go', 'build', '-trimpath', '-ldflags', flags, '-o', helper, './cmd/chora', cwd=ROOT, env=linux)
    helper.with_suffix('.sha256').write_text(hashlib.sha256(helper.read_bytes()).hexdigest() + '\n')
    run('xcrun', 'swiftc', '-target', 'arm64-apple-macosx13.0', '-O', '-module-cache-path', output / 'swift-cache', ROOT / 'desktop/macos/main.swift', ROOT / 'desktop/macos/Authentication.swift', ROOT / 'desktop/macos/RuntimeIntegrity.swift', '-o', app / 'Contents/MacOS/Chora')
    with (ROOT / 'desktop/macos/Info.plist').open('rb') as stream:
        info = plistlib.load(stream)
    info.update(CFBundleShortVersionString=args.version.split('-')[0], CFBundleVersion=str(args.build_number), ChoraReleaseVersion=args.version)
    with (app / 'Contents/Info.plist').open('wb') as stream:
        plistlib.dump(info, stream)
    shutil.copy2(ROOT / 'LICENSE', resources / 'LICENSE')
    shutil.copy2(ROOT / 'desktop/macos/auth.mjs', resources / 'auth.mjs')
    # Keep the dependency identity/license inventory with the application.
    with (resources / 'pi-sbom.cdx.json').open('w') as stream:
        run(runtime / 'bin/npm', 'sbom', '--prefix', consumer, '--sbom-format=cyclonedx', '--omit=dev', '--userconfig', config, '--globalconfig', global_config, env=environment, stdout=stream)
    (resources / 'build.json').write_text(json.dumps({'version': args.version, 'sourceCommit': source, 'dirtySource': dirty, 'sourceURL': f'https://github.com/Yangyang96/chora/tree/{source}', 'node': NODE_VERSION, 'nodeSHA256': NODE_SHA, 'npm': NPM_VERSION, 'pi': '0.85.1', 'distributionQualified': False}, indent=2) + '\n')
    identity = args.identity or '-'
    entitlement = output / 'runtime-entitlements.plist'
    with entitlement.open('wb') as stream:
        plistlib.dump({'com.apple.security.cs.allow-jit': True, 'com.apple.security.cs.allow-unsigned-executable-memory': True, 'com.apple.security.cs.disable-library-validation': True}, stream)
    # Sign every native dependency before the containing bundle. Never --deep sign.
    for path in sorted(resources.rglob('*')):
        if path.is_symlink() or not path.is_file():
            continue
        with path.open('rb') as stream:
            magic = stream.read(4)
        if magic in [b'\xcf\xfa\xed\xfe', b'\xfe\xed\xfa\xcf', b'\xca\xfe\xba\xbe', b'\xbe\xba\xfe\xca']:
            arguments = ['codesign', '--force', '--sign', identity]
            if args.identity:
                arguments += ['--options', 'runtime', '--timestamp']
            if path == runtime / 'bin/node':
                arguments += ['--entitlements', entitlement]
            run(*arguments, path)
    manifest = runtime_manifest(runtime)
    (resources / 'runtime-manifest.json').write_bytes(manifest)
    (resources / 'runtime-id.txt').write_text(hashlib.sha256(manifest).hexdigest() + '\n')
    arguments = ['codesign', '--force', '--sign', identity]
    if args.identity:
        arguments += ['--options', 'runtime', '--timestamp']
    run(*arguments, app)
    run('codesign', '--verify', '--deep', '--strict', app)
    if args.dmg:
        image_root = output / 'image'
        image_root.mkdir()
        shutil.copytree(app, image_root / 'Chora.app', symlinks=True)
        (image_root / 'Applications').symlink_to('/Applications')
        dmg = output / f'chora-{args.version}-macos-arm64.dmg'
        run('hdiutil', 'create', '-volname', 'Chora', '-srcfolder', image_root, '-format', 'UDZO', dmg)
        (output / (dmg.name + '.sha256')).write_text(hashlib.sha256(dmg.read_bytes()).hexdigest() + '  ' + dmg.name + '\n')
    print(json.dumps({'app': str(app), 'signedForDistribution': bool(args.identity), 'notarized': False}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--cache', type=Path, default=Path(tempfile.gettempdir()) / 'chora-package-downloads')
    parser.add_argument('--version', required=True)
    parser.add_argument('--build-number', type=int, default=1)
    parser.add_argument('--identity', help='Developer ID Application identity; omit for local ad-hoc testing')
    parser.add_argument('--dmg', action='store_true')
    build(parser.parse_args())


if __name__ == '__main__':
    main()
