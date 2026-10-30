#!/usr/bin/env python3
"""Archive committed Corresponding Source and its offline Go build inputs."""
from pathlib import Path
import argparse
import io
import json
import shlex
import subprocess
import tarfile
import tempfile

PRODUCTS = {
    'ai-dev-cycle-manager': ('devcycle', 'main.version'),
    'infrastructure-discovery': ('infrascout', 'github.com/GeneJie199/infrastructure-discovery/pkg/infrascout.Version'),
    'fleet-observability': ('fleetctl', 'main.version'),
    'release-validation': ('releaseguard', 'main.version'),
    'lifecycle-spec': ('lifecycle-validate', None),
}


def provenance(snapshot, revision):
    metadata = json.loads((snapshot / 'LICENSE_METADATA.json').read_text(encoding='utf-8'))
    result = {'revision': revision, 'license': metadata['sourceLicense'],
              'version': metadata['sourceVersion']}
    if not (snapshot / 'go.mod').exists():
        result.update(build='Script-only Suite launcher; no compilation required. Use scripts/install-suite.sh or scripts/install-suite.ps1 with suite-manifest.json. Build offline bundles with python3 scripts/build-offline-bundle.py --platform <platform> --output dist after obtaining manifest-pinned component assets.',
                      buildArgs=[], buildEnvironment={})
        return result
    module = next(line.split()[1] for line in (snapshot / 'go.mod').read_text().splitlines()
                  if line.startswith('module '))
    name = module.rsplit('/', 1)[-1]
    if name not in PRODUCTS:
        raise SystemExit('Unknown product module: ' + module)
    binary, version_symbol = PRODUCTS[name]
    flags = '-s -w'
    if version_symbol:
        flags += ' -X ' + version_symbol + '=' + result['version']
    args = ['go', 'build', '-mod=vendor', '-trimpath', '-ldflags', flags,
            '-o', 'dist/' + binary, './cmd/' + binary]
    result.update(module=module, toolchain=subprocess.check_output(['go', 'env', 'GOVERSION'], text=True).strip(),
                  build='CGO_ENABLED=0 ' + shlex.join(args), buildArgs=args,
                  buildEnvironment={'CGO_ENABLED': '0'},
                  platform='Set GOOS/GOARCH for the desired release target. For Windows append .exe to the -o filename.')
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
    if subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=no'], cwd=root, text=True).strip():
        raise SystemExit('Corresponding Source requires a clean committed checkout')
    payload = subprocess.check_output(['git', '-c', 'core.autocrlf=false', 'archive', '--format=tar', revision], cwd=root)
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='corresponding-source-') as work:
        snapshot = Path(work) / 'snapshot'
        snapshot.mkdir()
        with tarfile.open(fileobj=io.BytesIO(payload), mode='r:') as src:
            src.extractall(snapshot, filter='data')
        info = provenance(snapshot, revision)
        # Resolve imports from archived HEAD, never from untracked local files.
        if (snapshot / 'go.mod').exists():
            subprocess.run(['go', 'mod', 'vendor'], cwd=snapshot, check=True)
        with tarfile.open(output, 'w:gz') as tar:
            with tarfile.open(fileobj=io.BytesIO(payload), mode='r:') as src:
                for member in src.getmembers():
                    tar.addfile(member, src.extractfile(member) if member.isfile() else None)
            vendor = snapshot / 'vendor'
            if vendor.exists():
                tar.add(vendor, arcname='vendor')
            data = (json.dumps(info, indent=2) + '\n').encode()
            member = tarfile.TarInfo('SOURCE_PROVENANCE.json')
            member.size = len(data)
            tar.addfile(member, io.BytesIO(data))
    print(output)


if __name__ == '__main__':
    main()
