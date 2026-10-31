#!/usr/bin/env python3
"""License metadata and preserved-notice gate; uncertain classifications are warnings."""
import hashlib
import json
import pathlib
import sys

def check(root):
    errors=[]
    required=['LICENSE','LICENSES/AGPL-3.0-only.txt','LICENSES/Apache-2.0.txt','NOTICE','LICENSE_POLICY.md','COMMERCIAL_LICENSE.md','COMMERCIALIZATION.md','TRADEMARK_POLICY.md','CONTRIBUTOR_POLICY.md','LICENSING_HISTORY.md','THIRD_PARTY_NOTICES.md','LICENSE_METADATA.json','third-party-inventory.json','third-party.cdx.json']
    for name in required:
        if not (root/name).is_file(): errors.append('missing '+name)
    if errors:return errors
    license=(root/'LICENSE').read_bytes()
    if license!=(root/'LICENSES/AGPL-3.0-only.txt').read_bytes() or b'GNU AFFERO GENERAL PUBLIC LICENSE' not in license or len(license)<30000:errors.append('nonstandard or inconsistent AGPL text')
    if 'AGPL-3.0-only' not in (root/'README.md').read_text(encoding='utf-8'):errors.append('README license mismatch')
    metadata=json.loads((root/'LICENSE_METADATA.json').read_text(encoding='utf-8'))
    if metadata['sourceLicense']!='AGPL-3.0-only':errors.append('source metadata mismatch')
    inventory=json.loads((root/'third-party-inventory.json').read_text(encoding='utf-8'))
    for module in inventory['modules']:
        for item in module['licenseFiles']:
            path=(root/item['path']).resolve()
            if not path.is_relative_to(root.resolve()) or not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest()!=item['sha256']:errors.append('license evidence mismatch: '+item['path'])
    for warning in inventory['warnings']:print('WARNING:',warning)
    sbom=json.loads((root/'third-party.cdx.json').read_text(encoding='utf-8'))
    if sbom['metadata']['component']['licenses'][0]['license']['id']!='AGPL-3.0-only':errors.append('SBOM source license mismatch')
    return errors

if __name__=='__main__':
    root=pathlib.Path(sys.argv[1]) if len(sys.argv)>1 else pathlib.Path(__file__).resolve().parents[1]
    failures=check(root)
    for failure in failures:print('ERROR:',failure)
    print('License guard:', 'FAIL' if failures else 'PASS')
    raise SystemExit(bool(failures))
