import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import release
import source_image


class SourceImages(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        roots = json.loads((release.ROOT / 'trust/fixtures/roots.json').read_text())
        self.denied = next(r['pem'] for r in roots if r['sha256'].lower() == 'd7a7a0fb5d7e2731d771e9484ebcdef71d5f0c3e0a2948782bc83ee0ea699ef4').encode()
        self.kept = next(r['pem'] for r in roots if r['name'] == 'emSign Root CS CA - G3').encode()

    def archive(self, entries):
        path = self.folder / 'rootfs.tar'
        with tarfile.open(path, 'w') as output:
            for name, content in entries:
                member = tarfile.TarInfo(name)
                member.size = len(content)
                output.addfile(member, io.BytesIO(content))
        return path

    def test_final_store_accepts_preserved_anchor(self):
        source_image.verify_store(self.archive([(release.BUNDLE, self.kept)]))

    def test_final_store_rejects_denied_missing_malformed_and_alternate_roots(self):
        for entries in ([], [(release.BUNDLE, b'garbage')],
                        [(release.BUNDLE, self.kept + b'\n' + self.denied)],
                        [(release.BUNDLE, self.kept), ('etc/ssl/certs/extra.pem', self.denied)]):
            with self.subTest(entries=[e[0] for e in entries]):
                with self.assertRaises(ValueError):
                    source_image.verify_store(self.archive(entries))

    def test_failed_final_proof_prevents_release_tag_and_push(self):
        identity = 'sha256:' + 'a' * 64
        calls = []
        def fake_run(*args):
            calls.append(args)
            if args[:3] == ('docker', 'buildx', 'build'):
                Path(args[args.index('--iidfile') + 1]).write_text(identity)
            return identity
        for action in ('build', 'push'):
            calls.clear()
            with patch.object(source_image, 'run', side_effect=fake_run), patch.object(source_image, 'verify_image', side_effect=ValueError('dirty store')):
                with self.assertRaises(ValueError):
                    source_image.main(action, 'release:test')
            self.assertFalse(any(c[:2] in [('docker', 'tag'), ('docker', 'push')] for c in calls))

    def test_fresh_test_image_loaded_and_run_without_preexisting_tag(self):
        fake = self.folder / 'docker'
        fake.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
with open(os.environ['DOCKER_LOG'], 'a') as f:
    f.write(json.dumps(args) + '\\n')
if args[:2] == ['buildx', 'build']:
    assert '--load' in args
    assert args[args.index('--builder') + 1] == 'admitted-builder'
    if os.environ.get('BUILD_FAIL'):
        sys.exit(1)
    if not os.environ.get('MISSING_ID'):
        pathlib.Path(args[args.index('--iidfile') + 1]).write_text('sha256:' + 'b' * 64)
elif args[0] == 'run':
    assert args[-1] == 'sha256:' + 'b' * 64
else:
    sys.exit(2)
''')
        fake.chmod(0o755)
        log = self.folder / 'docker.jsonl'
        env = dict(os.environ, PATH=str(self.folder) + os.pathsep + os.environ['PATH'],
                   DOCKER_LOG=str(log), BUILDX_BUILDER='admitted-builder')
        script = str(release.ROOT / 'build/run-tests.sh')
        subprocess.run(['bash', script, 'unit'], cwd=self.folder, env=env, check=True, capture_output=True)
        calls = [json.loads(line) for line in log.read_text().splitlines()]
        self.assertEqual(len(calls), 2)
        self.assertEqual(calls[-1][-1], 'sha256:' + 'b' * 64)
        for failure in ('BUILD_FAIL', 'MISSING_ID'):
            log.unlink()
            result = subprocess.run(['bash', script], cwd=self.folder, env=dict(env, **{failure: '1'}), capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(len(log.read_text().splitlines()), 1)

    def test_vetted_sanitizer_preserves_other_roots(self):
        # No host trust refresh: all mutation stays inside the temporary fixture tree.
        refresh = self.folder / 'update-ca-certificates'
        refresh.write_text('#!/bin/sh\nexit 0\n')
        refresh.chmod(0o755)
        store = self.folder / 'store'
        store.mkdir()
        bundle = store / 'bundle.crt'
        bundle.write_bytes(self.denied + b'\n' + self.kept + b'\n')
        env = dict(os.environ, PATH=str(self.folder) + os.pathsep + os.environ['PATH'])
        subprocess.run(['sh', str(release.ROOT / 'trust/remove-rejected-cas.sh'),
                        '--policy', str(release.ROOT / 'trust/rejected-roots.sha256'),
                        '--bundle', str(bundle), '--scan-root', str(store)], env=env, check=True, capture_output=True)
        self.assertEqual(release.filtered(bundle.read_bytes()), release.filtered(self.kept))
        self.assertNotIn(self.denied.strip(), bundle.read_bytes())


if __name__ == '__main__':
    unittest.main()
