import os
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
