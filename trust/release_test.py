import os
import io
import tarfile
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import release


class ReleaseGuards(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        patcher = patch.dict(os.environ, EVIDENCE_DIR=self.temp.name)
        patcher.start()
        self.addCleanup(patcher.stop)

    def chart_archive(self, entries):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode='w:gz') as archive:
            for name, kind, content in entries:
                member = tarfile.TarInfo(name)
                member.type = kind
                if kind == tarfile.REGTYPE:
                    member.size = len(content)
                else:
                    member.linkname = content.decode()
                archive.addfile(member, io.BytesIO(content) if member.isfile() else None)
        return buffer.getvalue()

    def run_chart_archive(self, payload):
        out = Path(self.temp.name)
        tag = '20261106.1' + release.SUFFIX
        version = '0.1.123' + release.SUFFIX

        def helm(*args):
            if args[1] == 'package':
                with tarfile.open(out / ('quix-environment-operator-' + version + '.tgz'), 'w:gz') as archive:
                    archive.add(out / 'chart/quix-environment-operator', arcname='quix-environment-operator')
            if args[1] == 'template':
                image = 'customer/operator:customer-override' if '--set' in args else 'quixcontainerregistry.azurecr.io/quix-environment-operator:' + tag
                return 'image: "' + image + '"'
            return ''

        with patch.dict(os.environ, IMAGE_TAG=tag, CHART_VERSION=version), \
                patch.object(release, 'CHART_BLOB', release.hashlib.sha256(payload).hexdigest()), \
                patch.object(subprocess, 'check_output', return_value=payload), \
                patch.object(release, 'run', side_effect=helm):
            return release.chart(out)

    def test_chart_extracts_without_tarfile_filter_api(self):
        payload = self.chart_archive([
            ('quix-environment-operator', tarfile.DIRTYPE, b''),
            ('quix-environment-operator/values.yaml', tarfile.REGTYPE,
             b'repository: quixanalytics/quix-environment-operator\ntag: "0.1.6"\n'),
            ('quix-environment-operator/templates/operator.yaml', tarfile.REGTYPE, b'kind: Deployment\n'),
        ])
        # Python on the native worker lacks extraction filters. Do not fall back
        # to its unrestricted extraction API either.
        with patch.object(tarfile.TarFile, 'extractall', side_effect=AssertionError('unsafe extraction')), \
                patch.object(tarfile.TarFile, 'extract', side_effect=AssertionError('unsafe extraction')):
            self.assertTrue(self.run_chart_archive(payload).is_file())
        values = (Path(self.temp.name) / 'chart/quix-environment-operator/values.yaml').read_text()
        self.assertIn('repository: quixcontainerregistry.azurecr.io/quix-environment-operator', values)
        self.assertIn('tag: ""', values)

    def test_chart_rejects_unsafe_members_before_writing(self):
        for name, kind, content in [
            ('../escaped', tarfile.REGTYPE, b'bad'),
            ('quix-environment-operator/../../escaped', tarfile.REGTYPE, b'bad'),
            (str(Path(self.temp.name) / 'escaped'), tarfile.REGTYPE, b'bad'),
            ('quix-environment-operator/link', tarfile.SYMTYPE, b'../../escaped'),
            ('quix-environment-operator/link', tarfile.LNKTYPE, b'../escaped'),
            ('quix-environment-operator/fifo', tarfile.FIFOTYPE, b''),
            ('quix-environment-operator/device', tarfile.CHRTYPE, b''),
        ]:
            with self.subTest(name=name, kind=kind):
                payload = self.chart_archive([
                    ('quix-environment-operator/values.yaml', tarfile.REGTYPE, b'tag: "0.1.6"'),
                    (name, kind, content),
                ])
                with self.assertRaisesRegex(ValueError, 'unsafe chart member'):
                    self.run_chart_archive(payload)
                self.assertEqual(list((Path(self.temp.name) / 'chart').iterdir()), [])
                self.assertFalse((Path(self.temp.name) / 'escaped').exists())

    def test_unknown_registry_failure_is_not_absence(self):
        for error in ('UNAUTHORIZED', 'connection timeout', 'MANIFEST_UNKNOWN: DENIED', 'unknown'):
            with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', error)):
                with self.assertRaises(ValueError):
                    release.absent('test:tag')

    def test_existing_tag_refused(self):
        with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, '{}', '')):
            with self.assertRaises(ValueError):
                release.absent('test:tag')

    def test_explicit_absence_accepted(self):
        with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, '', 'MANIFEST_UNKNOWN')):
            release.absent('test:tag')

    def test_release_tag_required(self):
        for tag in ('latest', '0.1.6', 'custom-2026-09-spaces-hotfix-2'):
            with patch.dict(os.environ, IMAGE_TAG=tag, CHART_VERSION='0.1.123-2026-09-spaces-hotfix-2'):
                with self.assertRaises(ValueError):
                    release.versions()

    def test_helm_semver_separate_from_build_number(self):
        with patch.dict(os.environ, IMAGE_TAG='20261106.1-2026-09-spaces-hotfix-2', CHART_VERSION='0.1.123-2026-09-spaces-hotfix-2'):
            self.assertEqual(release.versions()[1], '0.1.123-2026-09-spaces-hotfix-2')

    def test_missing_or_malformed_bundle_refused(self):
        for bundle in (b'', b'garbage', b'-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----'):
            with self.assertRaises(Exception):
                release.filtered(bundle)

    def test_unapproved_publication_refused(self):
        with patch.dict(os.environ, PUBLISH='false'):
            with self.assertRaises(ValueError):
                release.publish(Path('/unused'))

    def test_filter_preserves_other_real_anchors(self):
        roots = json.loads((release.ROOT / 'trust/fixtures/roots.json').read_text())
        denied = next(r['pem'] for r in roots if r['sha256'].lower() == 'd7a7a0fb5d7e2731d771e9484ebcdef71d5f0c3e0a2948782bc83ee0ea699ef4')
        kept = next(r['pem'] for r in roots if r['name'] == 'emSign Root CS CA - G3')
        result = release.filtered((denied + '\n' + kept + '\n').encode())
        self.assertEqual(result, (kept + '\n').encode())
        self.assertEqual(release.filtered(result), result)

    def test_real_fixtures_integrity(self):
        self.assertTrue(release.verify_fixtures().is_dir())


if __name__ == '__main__':
    unittest.main()
