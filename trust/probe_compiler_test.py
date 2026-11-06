import importlib.util
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

ARCHES = ('amd64',)
COMPONENTS = {'operator': 'usr/bin/operator'}
MODULE = 'module historicalprobe\n\ngo 1.24.0\n'
PROBE_FILE = 'probe'
CACHE = False
ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('probe_release', ROOT / 'trust/release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


def metadata(version=None, arch='amd64', path='command-line-arguments', cgo='0'):
    return f'binary: {version or release.PROBE_GO}\n\tpath\t{path}\n\tbuild\tGOOS=linux\n\tbuild\tGOARCH={arch}\n\tbuild\tCGO_ENABLED={cgo}'


class ProbeCompilerTests(unittest.TestCase):
    def test_actual_consumer_metadata_rejects_version_platform_path_cgo(self):
        for arch in ARCHES:
            for changed in ({'version': 'go1.27.3'}, {'arch': 'wrong'}, {'path': 'other'}, {'cgo': '1'}):
                with self.subTest(arch=arch, changed=changed), patch.object(release, 'run', return_value=metadata(**(dict(arch=arch) | changed))):
                    with self.assertRaisesRegex(ValueError, 'metadata mismatch'):
                        release.go_metadata('binary', arch, release.PROBE_GO, 'command-line-arguments')
            with patch.object(release, 'run', return_value=metadata(arch=arch)):
                release.go_metadata('binary', arch, release.PROBE_GO, 'command-line-arguments')

    def test_acquisition_authenticates_and_scrubs_ambient_policy(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            archive = out / 'bad.zip'
            archive.write_bytes(b'untrusted')
            def run(*args, **kwargs):
                env = kwargs['env']
                for key in ('GOROOT', 'GOEXPERIMENT', 'GOAMD64', 'GOARM64', 'GODEBUG'):
                    self.assertNotIn(key, env)
                for key, value in {'GOENV':'off', 'GOTOOLCHAIN':'local', 'GOWORK':'off', 'GOSUMDB':'sum.golang.org', 'GONOSUMDB':'', 'GOINSECURE':''}.items():
                    self.assertEqual(env[key], value)
                if args[:2] == ('go', 'env'):
                    return json.dumps({'GOHOSTOS':'linux', 'GOHOSTARCH':'amd64'})
                return json.dumps({'Path':'golang.org/toolchain', 'Version':'v0.0.1-' + release.PROBE_GO + '.linux-amd64', 'Zip':str(archive)})
            with patch.dict(os.environ, dict(GODEBUG='x509sha1=1', GOAMD64='v4', GOARM64='v9.0', GOEXPERIMENT='bad', GOROOT='/wrong')), patch.object(release, 'run', side_effect=run):
                with self.assertRaisesRegex(ValueError, 'material identity mismatch'):
                    with release.probe_compiler(out):
                        self.fail('untrusted compiler executed')
            self.assertFalse((out / 'probe-compiler.json').exists())

    def test_actual_output_required_before_isolated_build(self):
        for component in COMPONENTS:
            for arch in ARCHES:
                for mismatch in (False, True):
                    with self.subTest(component=component, arch=arch, mismatch=mismatch), tempfile.TemporaryDirectory() as tmp:
                        out = Path(tmp)
                        folder = out / arch
                        folder.mkdir()
                        (out / 'probe-compiler.json').write_text('{}')
                        with tarfile.open(folder / 'rootfs.tar', 'w') as archive:
                            entry = tarfile.TarInfo(COMPONENTS[component])
                            entry.size = 8
                            archive.addfile(entry, io.BytesIO(b'consumer'))
                        calls = []
                        def run(*args, **kwargs):
                            calls.append(args)
                            if args[:3] == ('go', 'version', '-m'):
                                return metadata(version='go1.24.0' if mismatch else None, arch=arch) if args[-1].endswith('consumer') else metadata(arch=arch, path='historicalprobe')
                            self.assertEqual(args[0], '/authenticated/bin/go')
                            stage = kwargs['cwd']
                            self.assertEqual((stage / 'go.mod').read_text(), MODULE)
                            self.assertEqual(sorted(p.name for p in stage.iterdir()), ['go.mod', 'main.go'])
                            self.assertEqual(kwargs['env']['GOTOOLCHAIN'], 'local')
                            self.assertEqual(kwargs['env']['GOWORK'], 'off')
                            self.assertEqual(kwargs['env']['GOPROXY'], 'off')
                            self.assertEqual(kwargs['env']['GOARCH'], arch)
                            (folder / PROBE_FILE).write_bytes(b'probe')
                            return ''
                        with patch.object(release, 'COMPONENT', component, create=True), patch.object(release, 'run', side_effect=run):
                            if mismatch:
                                with self.assertRaisesRegex(ValueError, 'metadata mismatch'):
                                    release.build_probe(folder, arch, '/authenticated/bin/go', {'GOTOOLCHAIN':'local', 'GOWORK':'off'})
                                self.assertEqual(len(calls), 1)
                            else:
                                release.build_probe(folder, arch, '/authenticated/bin/go', {'GOTOOLCHAIN':'local', 'GOWORK':'off'})
                                evidence = json.loads((folder / 'probe-evidence.json').read_text())
                                self.assertEqual(evidence['consumer_sha256'], release.digest(folder / 'consumer'))
                                self.assertEqual(evidence['probe_sha256'], release.digest(folder / PROBE_FILE))
                                self.assertEqual(evidence['control'], 'original-store success under selected candidate verifier; final-store unknown-authority denial')

    def test_failed_candidate_clears_stale_success(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp)
            (out / 'qualified.json').write_text('stale')
            with patch.object(release, 'verify_candidate', side_effect=ValueError('candidate failure')):
                with self.assertRaisesRegex(ValueError, 'candidate failure'):
                    release.qualify(out)
            self.assertFalse((out / 'qualified.json').exists())

    def test_publication_rejects_tampered_probe_evidence(self):
        for changed in ('probe-compiler.json', 'probe-evidence.json', PROBE_FILE, 'consumer'):
            with self.subTest(changed=changed), tempfile.TemporaryDirectory() as tmp:
                out = Path(tmp)
                scope = out / 'operator' if CACHE else out
                scope.mkdir(exist_ok=True)
                (scope / 'probe-compiler.json').write_text('{}')
                compiler_hash = release.digest(scope / 'probe-compiler.json')
                platforms = {}
                for arch in ARCHES:
                    folder = scope / arch
                    folder.mkdir()
                    for name in ('tls.log', 'rootfs.tar', PROBE_FILE, 'consumer'):
                        (folder / name).write_text(name)
                    release.save(folder / 'probe-evidence.json', {'compiler_receipt_sha256':compiler_hash, 'probe_sha256':release.digest(folder / PROBE_FILE), 'consumer_sha256':release.digest(folder / 'consumer')})
                    platforms[arch] = {key: release.digest(folder / name) for name, key in [('probe-evidence.json','probe_evidence_sha256'), ('tls.log','tls_sha256'), ('rootfs.tar','rootfs_sha256')]}
                release.save(scope / 'qualified.json', {'platforms':platforms, 'probe_compiler_sha256':compiler_hash})
                target = scope / changed if changed == 'probe-compiler.json' else scope / ARCHES[0] / changed
                target.write_text('tampered')
                with patch.dict(os.environ, {'PUBLISH':'true'}), patch.object(release, 'verify_candidate', return_value={}), patch.object(release, 'run'), patch.object(release, 'absent') as registry:
                    with self.assertRaisesRegex(ValueError, 'evidence changed'):
                        (release.publish_all if CACHE else release.publish)(out)
                    registry.assert_not_called()
