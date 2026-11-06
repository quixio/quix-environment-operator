"""Local OCI qualification and immutable publication; no registry writes in validate."""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import sys
import tarfile

ROOT = Path(__file__).resolve().parents[1]
BASE = 'quixanalytics/quix-environment-operator@sha256:bada63e9fd09fa79b58a2b36601a5598128b4fbdbe5ccc6b5a432115855a3608'
SCAN_REVISION = '267fba898141dac80a9c94e046ed8ff219dc0f8b'
SUFFIX = '-2026-09-spaces-hotfix-2'
ORIGINAL_IMAGE = BASE
FIXTURE_MANIFEST_SHA256 = 'a7d3b2b98b272ec912ac2e2c7919cc1981a7b5ab9e7672e188542380423ee949'
ORIGINAL_BUNDLE_SHA256 = '714d457d580922dbf1d0be8bd35ba236a842b50b0072ae791582a19adef772a5'


TOOLS = {
    'skopeo': 'quay.io/skopeo/stable@sha256:47853bb9fb24202af9110531ebd6e43c5f97701254ca290596640290d17942f4',
    'crane': 'gcr.io/go-containerregistry/crane@sha256:e78770b31258a3846f878036d9c1f63fbe4c871f9f56990bf77fd95c013e3c1b',
}


def tool_args(name, *args):
    out = Path(os.environ['EVIDENCE_DIR']).resolve()
    empty = out / 'empty-ca'; empty.mkdir(exist_ok=True)
    auth = Path(os.environ.get('DOCKER_CONFIG', str(Path.home() / '.docker'))).resolve()
    return ['docker', 'run', '--rm', '--platform', 'linux/amd64', '--user', str(os.getuid()) + ':' + str(os.getgid()),
        '-v', str(out) + ':' + str(out), '-v', str(auth) + ':/auth:ro',
        '-v', '/etc/ssl/certs/ca-certificates.crt:/qualification-ca.pem:ro',
        '-v', str(empty) + ':/empty-ca:ro', '-e', 'SSL_CERT_FILE=/qualification-ca.pem', '-e', 'SSL_CERT_DIR=/empty-ca',
        '-e', 'DOCKER_CONFIG=/auth', '-e', 'REGISTRY_AUTH_FILE=/auth/config.json', TOOLS[name], *args]



def run(*args, **kwargs):
    if args[0] in TOOLS:
        args = tool_args(*args)
    try:
        return subprocess.check_output(args, text=True, **kwargs).strip()
    except subprocess.CalledProcessError as error:
        if error.output:
            print(error.output, file=sys.stderr)
        raise


def digest(path):
    h = hashlib.sha256()
    with Path(path).open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def save(path, data):
    Path(path).write_text(json.dumps(data, indent=2) + '\n')


def verify_fixtures():
    fixtures = Path(os.environ.get('TRUST_FIXTURES', str(ROOT / 'trust/fixtures'))).resolve()
    if digest(fixtures / 'manifest.json') != FIXTURE_MANIFEST_SHA256:
        raise ValueError('unreviewed fixture manifest')
    manifest = json.loads((fixtures / 'manifest.json').read_text())
    for name, expected in manifest['files'].items():
        if digest(fixtures / name) != expected:
            raise ValueError('fixture checksum mismatch: ' + name)
    return fixtures


def prepare(out):
    (out / 'prepared.json').unlink(missing_ok=True)
    verify_fixtures()
    commands = [('git', '--version'), ('make', '--version'), ('go', 'version'),
                ('helm', 'version', '--short'), ('skopeo', '--version'), ('crane', 'version'),
                ('docker', 'version'), ('docker', 'buildx', 'version')]
    tools = {}
    for command in commands:
        if command[0] not in TOOLS and not shutil.which(command[0]):
            raise ValueError('required worker tool is not installed: ' + command[0])
        tools[' '.join(command)] = run(*command)
    if run('docker', 'info', '--format', '{{.OSType}}') != 'linux':
        raise ValueError('qualification requires a Linux Docker daemon')
    builder = run('docker', 'buildx', 'inspect', os.environ['BUILDX_BUILDER'])
    if not re.search(r'Driver:\s+docker-container\b', builder):
        raise ValueError('isolated docker-container builder required')
    platforms = set(re.findall(r'linux/(?:amd64|arm64)\b', builder))
    if 'linux/amd64' not in platforms:
        raise ValueError('selected buildx builder must support linux/amd64')
    config = json.loads(run('crane', 'config', ORIGINAL_IMAGE))
    if config['architecture'] != 'amd64' or config['os'] != 'linux':
        raise ValueError('unexpected baseline platform')
    save(out / 'original-config.json', config)
    archive = out / 'original-rootfs.tar'
    baseline = out / 'original-ca-certificates.crt'
    baseline.unlink(missing_ok=True)
    run('crane', 'export', '--platform', 'linux/amd64', ORIGINAL_IMAGE, str(archive))
    with tarfile.open(archive) as rootfs:
        members = [m for m in rootfs.getmembers() if m.name.removeprefix('./') == 'etc/ssl/certs/ca-certificates.crt']
        if len(members) != 1 or not members[0].isfile():
            raise ValueError('original image must contain one regular CA bundle')
        bundle = rootfs.extractfile(members[0]).read()
        stores = [m.name.removeprefix('./') for m in rootfs if m.isfile() and (m.name.endswith(('.crt', '.pem', '.cer', '.der', '.jks', '.p12')) or m.name.startswith(('etc/ssl/certs/', 'etc/pki/'))) ]
        if stores != [BUNDLE]:
            raise ValueError('unreviewed baseline trust stores: ' + repr(stores))
    if hashlib.sha256(bundle).hexdigest() != ORIGINAL_BUNDLE_SHA256:
        raise ValueError('original bundle differs from shipped 0.1.6 baseline')
    baseline.write_bytes(bundle)
    save(out / 'prepared.json', {'original_image': ORIGINAL_IMAGE, 'platform': 'linux/amd64',
                                'original_bundle_sha256': digest(baseline),
                                'fixture_manifest_sha256': FIXTURE_MANIFEST_SHA256,
                                'tools': tools, 'builder': builder})


def versions():
    if (os.environ.get('IMAGE_REGISTRY', 'quixcontainerregistry.azurecr.io') != 'quixcontainerregistry.azurecr.io'
            or os.environ.get('IMAGE_NAME', 'quix-environment-operator') != 'quix-environment-operator'):
        raise ValueError('release image repository must be quixcontainerregistry.azurecr.io/quix-environment-operator')
    tag = os.environ.get('IMAGE_TAG', '')
    chart = os.environ.get('CHART_VERSION', '')
    if not re.fullmatch(r'[0-9]+(?:\.[0-9]+)*' + SUFFIX, tag):
        raise ValueError('IMAGE_TAG must be an explicit numeric producer build plus ' + SUFFIX)
    if not re.fullmatch(r'0\.1\.(?:0|[1-9][0-9]*)' + SUFFIX, chart):
        raise ValueError('CHART_VERSION must be 0.1.<numeric build ID>' + SUFFIX)
    return tag, chart


def absent(ref):
    result = subprocess.run(tool_args('crane', 'manifest', ref), capture_output=True, text=True)
    if result.returncode == 0:
        raise ValueError('immutable tag already exists: ' + ref)
    # Registry authentication, transport and permission errors never prove absence.
    error = result.stderr
    if not re.search(r'\bMANIFEST_UNKNOWN\b', error) or re.search(r'UNAUTHORIZED|DENIED|FORBIDDEN|timeout|connection', error, re.I):
        raise ValueError('could not prove tag absence: ' + ref + ': ' + error)


def source():
    revision = run('git', 'rev-parse', 'HEAD', cwd=ROOT)
    if os.environ.get('SOURCE_REVISION', revision) != revision:
        raise ValueError('source revision mismatch')
    if run('git', 'status', '--porcelain', '--untracked-files=normal', cwd=ROOT):
        raise ValueError('candidate source must be committed and clean')
    return revision


CHART_BLOB = 'be9eff2cbfd46d484e30076c1a20db28e3ea05f3b88424b5069c7b72099fab12'
BUNDLE = 'etc/ssl/certs/ca-certificates.crt'
BINARY = 'usr/bin/operator'
BINARY_SHA256 = 'c7eb891a8221e6b352edf16aba5dda33e6e04bb8be88c76092cb89deea546ec3'


def filtered(bundle):
    import ssl
    policy = {line for line in (ROOT / 'trust/rejected-roots.sha256').read_text().splitlines() if line and not line.startswith('#')}
    if policy != {'7dd78d5f4f13459a83dff9abbba62edbaf6f2d102bf257fd712f4d9f2746ed8d', 'd7a7a0fb5d7e2731d771e9484ebcdef71d5f0c3e0a2948782bc83ee0ea699ef4', '86a1ecba089c4a8d3bbe2734c612ba341d813e043cf9e8a862cd5c57a36bbe6b'}:
        raise ValueError('unreviewed policy')
    certs = re.findall(rb'-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----', bundle, re.S)
    if not certs or re.sub(rb'-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----', b'', bundle, flags=re.S).strip():
        raise ValueError('invalid PEM bundle')
    kept = [c for c in certs if hashlib.sha256(ssl.PEM_cert_to_DER_cert(c.decode())).hexdigest() not in policy]
    if not kept:
        raise ValueError('empty effective trust store')
    return b'\n'.join(kept) + b'\n'


def inventory(archive):
    result = {}
    with tarfile.open(archive) as t:
        for m in t.getmembers():
            name = m.name.removeprefix('./').rstrip('/')
            if not name or name in {'.', 'etc/hosts', 'etc/hostname', 'etc/resolv.conf', '.dockerenv'}:
                continue
            result[name] = (m.type.decode(), m.mode, m.uid, m.gid, m.linkname,
                            hashlib.sha256(t.extractfile(m).read()).hexdigest() if m.isfile() else '')
    return result


def chart(out):
    tag, version = versions()
    archive = out / 'original-chart.tgz'
    archive.write_bytes(subprocess.check_output(tool_args('crane', 'blob', 'quixcontainerregistry.azurecr.io/helm/quix-environment-operator@sha256:' + CHART_BLOB)))
    if digest(archive) != CHART_BLOB:
        raise ValueError('chart baseline content mismatch')
    stage = out / 'chart'
    if stage.exists():
        shutil.rmtree(stage)
    stage.mkdir()
    with tarfile.open(archive) as t:
        members = t.getmembers()
        # Helm packages contain only regular files and directories. Validate all
        # entries before writing; extraction filters are unavailable on workers.
        for member in members:
            path = PurePosixPath(member.name)
            if (path.is_absolute() or '..' in path.parts or not path.parts
                    or not (member.isfile() or member.isdir())):
                raise ValueError('unsafe chart member: ' + member.name)
        for member in members:
            target = stage / member.name
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with t.extractfile(member) as source, target.open('wb') as destination:
                    shutil.copyfileobj(source, destination)
    chartdir = stage / 'quix-environment-operator'
    values = chartdir / 'values.yaml'
    values.write_text(values.read_text().replace('repository: quixanalytics/quix-environment-operator', 'repository: quixcontainerregistry.azurecr.io/quix-environment-operator').replace('tag: "0.1.6"', 'tag: ""'))
    run('helm', 'package', str(chartdir), '--version', version, '--app-version', tag, '--destination', str(out))
    package = out / ('quix-environment-operator-' + version + '.tgz')
    run('helm', 'lint', str(package))
    rendered = run('helm', 'template', 'operator', str(package))
    if 'image: "quixcontainerregistry.azurecr.io/quix-environment-operator:' + tag + '"' not in rendered:
        raise ValueError('packaged chart must select qualified image')
    override = run('helm', 'template', 'operator', str(package), '--set', 'image.repository=customer/operator,image.tag=customer-override')
    if 'image: "customer/operator:customer-override"' not in override:
        raise ValueError('customer override lost')
    # All template bytes, including CRD and RBAC, come from the released package.
    with tarfile.open(archive) as original, tarfile.open(package) as final:
        expected = {m.name: original.extractfile(m).read() for m in original if '/templates/' in m.name and m.isfile()}
        actual = {m.name: final.extractfile(m).read() for m in final if '/templates/' in m.name and m.isfile()}
        if expected != actual:
            raise ValueError('released templates changed')
    (out / 'rendered.yaml').write_text(rendered)
    return package


def candidate(out):
    tag, version = versions()
    revision = source()
    if (out / 'candidate.oci.tar').exists():
        raise ValueError('candidate already exists; use a new evidence directory')
    baseline = out / 'original-ca-certificates.crt'
    if digest(baseline) != ORIGINAL_BUNDLE_SHA256:
        raise ValueError('prepare must acquire pinned baseline first')
    context = out / 'context'
    context.mkdir(exist_ok=True)
    (context / 'ca-certificates.crt').write_bytes(filtered(baseline.read_bytes()))
    shutil.copy2(ROOT / 'build/hotfix2.Dockerfile', context / 'Dockerfile')
    run('docker', 'buildx', 'build', '--builder', os.environ['BUILDX_BUILDER'], '--platform', 'linux/amd64', '--provenance=mode=max',
        '--metadata-file', str(out / 'build-metadata.json'), '--output', 'type=oci,dest=' + str(out / 'candidate.oci.tar'), str(context))
    package = chart(out)
    save(out / 'candidate.json', {'source': revision, 'base': BASE, 'image_tag': tag, 'chart_version': version,
                                 'build_url': os.environ.get('BUILD_URL', ''), 'oci_sha256': digest(out / 'candidate.oci.tar'),
                                 'index_digest': 'sha256:' + hashlib.sha256(subprocess.check_output(tool_args('skopeo', 'inspect', '--raw', 'oci-archive:' + str(out / 'candidate.oci.tar')))).hexdigest(),
                                 'chart_sha256': digest(package)})


def verify_candidate(out):
    tag, version = versions()
    receipt = json.loads((out / 'candidate.json').read_text())
    if receipt['source'] != source() or receipt['image_tag'] != tag or receipt['chart_version'] != version:
        raise ValueError('candidate identity mismatch')
    for path, key in [(out / 'candidate.oci.tar', 'oci_sha256'), (out / ('quix-environment-operator-' + version + '.tgz'), 'chart_sha256')]:
        if digest(path) != receipt[key]:
            raise ValueError('candidate content changed: ' + str(path))
    return receipt


def qualify(out):
    (out / 'qualified.json').unlink(missing_ok=True)
    receipt = verify_candidate(out)
    fixtures = verify_fixtures()
    baseline = out / 'original-ca-certificates.crt'
    if digest(baseline) != ORIGINAL_BUNDLE_SHA256:
        raise ValueError('original bundle differs from shipped 0.1.6 baseline')
    buildsteps = Path(os.environ['BUILDSTEPS_DIR']).resolve()
    if run('git', 'rev-parse', 'HEAD', cwd=buildsteps) != SCAN_REVISION or run('git', 'status', '--porcelain', cwd=buildsteps):
        raise ValueError('BuildSteps must be the clean reviewed pinned revision')
    # Remove previous success before any operation that may fail.
    (out / 'qualified.json').unlink(missing_ok=True)
    results = {}
    for arch in ('amd64',):
        folder = out / arch
        folder.mkdir(exist_ok=True)
        shutil.copy2(ROOT / 'trust/rejected-roots.sha256', folder)
        shutil.copy2(baseline, folder / 'original-ca-certificates.crt')
        env = dict(os.environ, GOOS='linux', GOARCH=arch, CGO_ENABLED='0')
        run('go', 'build', '-o', str(folder / 'probe'), './trust/probe', cwd=ROOT, env=env)
        local = 'operator-qualified-' + arch
        number = receipt['oci_sha256'][:32]
        image = local + ':' + number
        archive = folder / 'image.tar'
        run('skopeo', '--override-os', 'linux', '--override-arch', arch, 'copy', 'oci-archive:' + str(out / 'candidate.oci.tar'), 'docker-archive:' + str(archive) + ':' + image)
        run('docker', 'load', '--input', str(archive))
        image_id = run('docker', 'image', 'inspect', '--format', '{{.Id}}', image)
        original_config = json.loads((out / 'original-config.json').read_text())['config']
        final_config = json.loads(run('docker', 'image', 'inspect', image_id))[0]['Config']
        for key in ('User', 'Env', 'Entrypoint', 'WorkingDir', 'Cmd'):
            if original_config.get(key) != final_config.get(key):
                raise ValueError('runtime configuration changed: ' + key)
        container = run('docker', 'create', '--platform', 'linux/' + arch, image_id)
        try:
            run('docker', 'export', '--output', str(folder / 'rootfs.tar'), container)
        finally:
            run('docker', 'rm', container)
        original = inventory(out / 'original-rootfs.tar')
        final = inventory(folder / 'rootfs.tar')
        if original.pop(BUNDLE)[-1] != ORIGINAL_BUNDLE_SHA256:
            raise ValueError('baseline rootfs mismatch')
        final.pop(BUNDLE)
        if final != original or final[BINARY][-1] != BINARY_SHA256:
            raise ValueError('overlay changed files beyond trust bundle')
        with tarfile.open(folder / 'rootfs.tar') as t:
            bundle = t.extractfile(BUNDLE).read()
        if bundle != filtered(baseline.read_bytes()):
            raise ValueError('candidate trust bundle differs from reviewed filter')
        log = run('docker', 'run', '--rm', '--platform', 'linux/' + arch, '--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid',
                  '-v', str(folder) + ':/qualification:ro', '-v', str(fixtures) + ':/fixtures:ro',
                  '--entrypoint', '/qualification/probe', image_id)
        (folder / 'tls.log').write_text(log + '\n')
        env = dict(os.environ, SCAN_REPOSITORY=local, SCAN_BUILD_NUMBER=number)
        run('python3', str(buildsteps / 'docker/scan_materials.py'), cwd=folder, env=env)
        scan = json.loads((folder / ('trivy-materials-' + local + '.' + number + '.json')).read_text())
        report = json.loads((folder / ('trivy-report-' + local + '.json')).read_text())
        if scan['status'] != 'PASS' or scan['image'] != image or run('docker', 'image', 'inspect', '--format', '{{.Id}}', image) != image_id:
            raise ValueError('scan evidence/image mismatch')
        findings = [v for result in report.get('Results', []) for v in result.get('Vulnerabilities', [])]
        counts = {s: sum(v['Severity'] == s for v in findings) for s in ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')}
        if any(counts[s] for s in ('MEDIUM', 'HIGH', 'CRITICAL')):
            print('##vso[task.logissue type=warning]Operator ' + arch + ' vulnerabilities: ' + json.dumps(counts))
        results[arch] = {'image_id': image_id, 'rootfs_sha256': digest(folder / 'rootfs.tar'), 'tls_sha256': digest(folder / 'tls.log'), 'vulnerabilities': counts, 'scanner_revision': SCAN_REVISION, 'scan_materials_sha256': digest(folder / ('trivy-materials-' + local + '.' + number + '.json')), 'scan_report_sha256': digest(folder / ('trivy-report-' + local + '.json'))}
    tests = run('make', 'docker-test-unit', 'docker-test-integration', cwd=ROOT)
    (out / 'source-tests.log').write_text(tests + '\n')
    receipt['source_tests_sha256'] = digest(out / 'source-tests.log')
    receipt['platforms'] = results
    save(out / 'qualified.json', receipt)


def publish(out):
    if os.environ.get('PUBLISH') != 'true':
        raise ValueError('publication requires PUBLISH=true after independent review')
    receipt = verify_candidate(out)
    qualified = json.loads((out / 'qualified.json').read_text())
    if any(qualified.get(k) != v for k, v in receipt.items()) or set(qualified.get('platforms', {})) != {'amd64'}:
        raise ValueError('the exact candidate architecture must be qualified')
    if digest(out / 'source-tests.log') != qualified['source_tests_sha256']:
        raise ValueError('source test evidence changed')
    for arch, evidence in qualified['platforms'].items():
        folder = out / arch
        local = 'operator-qualified-' + arch
        number = receipt['oci_sha256'][:32]
        for file, key in [('rootfs.tar', 'rootfs_sha256'), ('tls.log', 'tls_sha256'),
                          ('trivy-materials-' + local + '.' + number + '.json', 'scan_materials_sha256'),
                          ('trivy-report-' + local + '.json', 'scan_report_sha256')]:
            if digest(folder / file) != evidence[key]:
                raise ValueError('qualification evidence changed: ' + file)
    image = os.environ.get('IMAGE_REGISTRY', 'quixcontainerregistry.azurecr.io') + '/' + os.environ.get('IMAGE_NAME', 'quix-environment-operator') + ':' + receipt['image_tag']
    chart_registry = os.environ.get('CHART_REGISTRY', 'quixcontainerregistry.azurecr.io/helm')
    chart_ref = chart_registry + '/quix-environment-operator:' + receipt['chart_version']
    absent(image)
    absent(chart_ref)
    state = {'status': 'publication-started', 'image': image, 'chart': chart_ref, 'source': receipt['source']}
    save(out / 'publication.json', state)
    try:
        run('skopeo', 'copy', '--all', '--preserve-digests', 'oci-archive:' + str(out / 'candidate.oci.tar'), 'docker://' + image)
        state.update(status='image-published', image_digest=run('crane', 'digest', image))
        save(out / 'publication.json', state)
        if state['image_digest'] != receipt['index_digest']:
            raise ValueError('published image digest differs from qualified candidate')
        absent(chart_ref)
        # Helm reads Docker's registry credential store; no password on the command line.
        env = dict(os.environ, HELM_REGISTRY_CONFIG=str(Path(os.environ.get('DOCKER_CONFIG', str(Path.home() / '.docker'))) / 'config.json'))
        run('helm', 'push', str(out / ('quix-environment-operator-' + receipt['chart_version'] + '.tgz')), 'oci://' + chart_registry, env=env)
        manifest = json.loads(run('crane', 'manifest', chart_ref))
        if not any(layer['digest'] == 'sha256:' + receipt['chart_sha256'] for layer in manifest['layers']):
            raise ValueError('published chart content differs from qualified package')
        state.update(status='published', chart_digest=run('crane', 'digest', chart_ref))
        (out / 'published-index.json').write_text(run('crane', 'manifest', image))
    finally:
        save(out / 'publication.json', state)


def main():
    out = Path(os.environ.get('EVIDENCE_DIR', str(ROOT / '.evidence'))).resolve()
    out.mkdir(parents=True, exist_ok=True)
    os.environ['EVIDENCE_DIR'] = str(out)
    action = sys.argv[1]
    if action == 'validate':
        prepare(out)
        candidate(out)
        qualify(out)
    else:
        {'prepare': prepare, 'candidate': candidate, 'qualify': qualify, 'chart': chart, 'publish': publish}[action](out)


if __name__ == '__main__':
    main()
