"""Gate ordinary source image tagging and publication on its actual runtime store."""
import os
from pathlib import Path
import re
import ssl
import subprocess
import sys
import tarfile
import tempfile

from release import BUNDLE, ROOT, filtered


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def verify_store(archive):
    with tarfile.open(archive) as rootfs:
        stores = [m for m in rootfs if (m.isfile() or m.issym() or m.islnk()) and (
            m.name.removeprefix('./').startswith(('etc/ssl/', 'etc/pki/', 'usr/share/ca-certificates/', 'usr/local/share/ca-certificates/'))
            or m.name.endswith(('.crt', '.pem', '.cer', '.der', '.jks', '.p12')))]
        if len(stores) != 1 or stores[0].name.removeprefix('./') != BUNDLE or not stores[0].isfile():
            raise ValueError('unexpected final runtime trust stores')
        bundle = rootfs.extractfile(stores[0]).read()
    clean = filtered(bundle)
    certs = re.findall(rb'-----BEGIN CERTIFICATE-----.*?-----END CERTIFICATE-----', bundle, re.S)
    if clean != b'\n'.join(certs) + b'\n':
        raise ValueError('denied root in final runtime')
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    context.load_verify_locations(cadata=clean.decode())
    if not context.get_ca_certs():
        raise ValueError('no usable final runtime roots')


def verify_image(image, folder):
    container = run('docker', 'create', '--platform', 'linux/amd64', image)
    archive = folder / 'rootfs.tar'
    try:
        run('docker', 'export', '--output', str(archive), container)
    finally:
        run('docker', 'rm', container)
    verify_store(archive)


def main(action, tag):
    with tempfile.TemporaryDirectory() as temporary:
        folder = Path(temporary)
        if action == 'build':
            builder = ['--builder', os.environ['BUILDX_BUILDER']] if os.environ.get('BUILDX_BUILDER') else []
            run('docker', 'buildx', 'build', *builder, '--platform', 'linux/amd64', '--load',
                '--iidfile', str(folder / 'image-id'), '-f', str(ROOT / 'build/dockerfile'), str(ROOT))
            image = (folder / 'image-id').read_text().strip()
        elif action == 'push':
            image = run('docker', 'image', 'inspect', '--format', '{{.Id}}', tag)
        else:
            raise ValueError('expected build or push')
        if not re.fullmatch(r'sha256:[0-9a-f]{64}', image):
            raise ValueError('missing fresh image identity')
        verify_image(image, folder)
        if action == 'build':
            run('docker', 'tag', image, tag)
        else:
            if run('docker', 'image', 'inspect', '--format', '{{.Id}}', tag) != image:
                raise ValueError('image tag changed during verification')
            run('docker', 'push', tag)


if __name__ == '__main__':
    main(*sys.argv[1:])
