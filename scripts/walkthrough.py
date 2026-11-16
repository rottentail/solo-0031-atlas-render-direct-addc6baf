#!/usr/bin/env python3
"""Run bounded production flows against a temporary Atlas Render server."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import zlib

ROOT = Path(__file__).resolve().parents[1]


def sample():
    return {"width": 320, "height": 240, "background": "#F5F1E8", "layers": [
        {"id": "plate", "kind": "rectangle", "width": 220, "height": 150,
         "transform": {"x": 45, "y": 35, "scaleX": 1, "scaleY": 1, "degrees": 5},
         "opacity": 1, "fill": {"kind": "linear", "color": "#145A70", "endColor": "#32A5A0", "from": {"x": 0, "y": 0}, "to": {"x": 220, "y": 150}}},
        {"id": "accent", "kind": "ellipse", "width": 130, "height": 130,
         "transform": {"x": 150, "y": 95, "scaleX": 1, "scaleY": 1, "degrees": 0},
         "opacity": .85, "fill": {"kind": "solid", "color": "#E87E4F"}},
        {"id": "inset", "kind": "group", "transform": {"x": 150, "y": 100, "scaleX": 1, "scaleY": 1, "degrees": -8}, "opacity": .9,
         "clip": {"x": 0, "y": 0, "width": 90, "height": 75}, "children": [
             {"id": "facet", "kind": "polygon", "width": 100, "height": 100,
              "transform": {"scaleX": 1, "scaleY": 1}, "opacity": .8,
              "fill": {"kind": "solid", "color": "#FFF4D5"}, "points": [{"x": 10, "y": 90}, {"x": 50, "y": 0}, {"x": 100, "y": 90}]}]}]}


def tiny_png():
    def chunk(tag, data):
        return struct.pack('>I', len(data)) + tag + data + struct.pack('>I', zlib.crc32(tag + data))
    return b'\x89PNG\r\n\x1a\n' + chunk(b'IHDR', struct.pack('>IIBBBBB', 2, 2, 8, 6, 0, 0, 0)) + chunk(b'IDAT', zlib.compress(b'\0' + bytes([240, 40, 80, 255] * 2) + b'\0' + bytes([40, 80, 240, 128] * 2))) + chunk(b'IEND', b'')


# --- Canonical JSON, matching Go's encoding/json for atlas payloads --------
# encoding/json emits struct fields in declaration order with omitempty rules
# and map keys in sorted order. The helpers below reproduce that so a test can
# build a package whose declared digests agree with the server's.

def _scalar(value):
    if value is None:
        return 'null'
    if isinstance(value, bool):
        return 'true' if value else 'false'
    if isinstance(value, int):
        return str(value)
    if isinstance(value, float):
        if value == int(value) and abs(value) < 1e21:
            return str(int(value))
        return repr(value)
    if isinstance(value, str):
        return json.dumps(value, ensure_ascii=False)
    raise TypeError(f'unsupported scalar {value!r}')


def _point(p):
    return '{"x":%s,"y":%s}' % (_scalar(p.get('x', 0)), _scalar(p.get('y', 0)))


def _rect(r):
    return '{"x":%s,"y":%s,"width":%s,"height":%s}' % tuple(_scalar(r[k]) for k in ('x', 'y', 'width', 'height'))


def _transform(t):
    return '{"x":%s,"y":%s,"scaleX":%s,"scaleY":%s,"degrees":%s}' % tuple(
        _scalar(t.get(k, 0)) for k in ('x', 'y', 'scaleX', 'scaleY', 'degrees'))


def _paint(p):
    text = '{"kind":%s,"color":%s' % (_scalar(p['kind']), _scalar(p['color']))
    if p.get('endColor'):
        text += ',"endColor":%s' % _scalar(p['endColor'])
    return text + ',"from":%s,"to":%s}' % (_point(p.get('from', {})), _point(p.get('to', {})))


def _layer(l):
    text = '{"id":%s,"kind":%s,"width":%s,"height":%s,"transform":%s,"opacity":%s,"fill":%s' % (
        _scalar(l['id']), _scalar(l['kind']), _scalar(l.get('width', 0)), _scalar(l.get('height', 0)),
        _transform(l.get('transform', {})), _scalar(l.get('opacity', 0)),
        _paint(l.get('fill', {'kind': '', 'color': ''})))
    if l.get('points'):
        text += ',"points":[' + ','.join(_point(p) for p in l['points']) + ']'
    if l.get('assetId'):
        text += ',"assetId":%s' % _scalar(l['assetId'])
    if l.get('clip'):
        text += ',"clip":' + _rect(l['clip'])
    if l.get('children'):
        text += ',"children":[' + ','.join(_layer(c) for c in l['children']) + ']'
    return text + '}'


def _document(d):
    return '{"width":%s,"height":%s,"background":%s,"layers":[%s]}' % (
        _scalar(d['width']), _scalar(d['height']), _scalar(d['background']),
        ','.join(_layer(l) for l in d['layers']))


def _asset(a):
    return '{"id":%s,"digest":%s,"state":%s,"width":%s,"height":%s,"png":%s}' % tuple(
        _scalar(a[k]) for k in ('id', 'digest', 'state', 'width', 'height', 'png'))


def _assets(assets):
    return '{' + ','.join(json.dumps(k) + ':' + _asset(v) for k, v in sorted(assets.items())) + '}'


def snapshot_digest(document, assets):
    """model.SnapshotDigest: digest of {Document, Assets}."""
    body = '{"Document":%s,"Assets":%s}' % (_document(document), _assets(assets))
    return hashlib.sha256(body.encode()).hexdigest()


def package_digest(snapshot):
    """bundle.Export: digest of the canonical snapshot record."""
    body = '{"id":%s,"sceneId":%s,"revision":%s,"state":%s,"document":%s,"assets":%s,"digest":%s,"createdAt":%s}' % (
        _scalar(snapshot['id']), _scalar(snapshot['sceneId']), _scalar(snapshot['revision']),
        _scalar(snapshot['state']), _document(snapshot['document']), _assets(snapshot['assets']),
        _scalar(snapshot['digest']), _scalar(snapshot['createdAt']))
    return hashlib.sha256(body.encode()).hexdigest()



class Demo:
    def __init__(self, binary, directory):
        self.binary = binary
        self.directory = directory
        self.proc = None
        self.log = open(directory / 'server.log', 'ab')
        self.start()

    def start(self):
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        self.url = f'http://127.0.0.1:{port}'
        env = dict(os.environ, ATLAS_ADDR=f'127.0.0.1:{port}', ATLAS_STATE=str(self.directory / 'state.json'))
        self.proc = subprocess.Popen([str(self.binary)], env=env, stdout=self.log, stderr=self.log)
        for _ in range(100):
            try:
                self.call('GET', '/healthz')
                return
            except (urllib.error.URLError, ConnectionError):
                if self.proc.poll() is not None:
                    raise RuntimeError((self.directory / 'server.log').read_text())
                time.sleep(.03)
        raise RuntimeError('server startup deadline exceeded')

    def stop(self, abrupt=False):
        if self.proc and self.proc.poll() is None:
            (self.proc.kill if abrupt else self.proc.terminate)()
            self.proc.wait(timeout=15)

    def call(self, method, path, body=None, status=200, headers=None):
        hdr = dict(headers or {})
        if isinstance(body, (dict, list)):
            body = json.dumps(body).encode()
            hdr['Content-Type'] = 'application/json'
        request = urllib.request.Request(self.url + path, data=body, method=method, headers=hdr)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            if response.status != status:
                raise RuntimeError(f'{method} {path}: expected {status}, received {response.status}: {data[:500]!r}')
            if response.headers.get('Content-Type', '').startswith('application/json'):
                return json.loads(data)
            return data

    def diagnose(self, method, path, body, status, headers=None):
        merged = {'Accept': 'application/vnd.atlas.diagnostics.v2+json', 'Content-Type': 'application/json'}
        merged.update(headers or {})
        request = urllib.request.Request(self.url + path, method=method,
                                         data=None if body is None else json.dumps(body).encode(),
                                         headers=merged)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            if response.status != status:
                raise RuntimeError(f'{method} {path}: expected {status}, received {response.status}: {data[:500]!r}')
            return json.loads(data)

    def wait(self, job_id, wanted=('succeeded',)):
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            job = self.call('GET', '/v1/jobs/' + job_id)
            if job['state'] in wanted:
                return job
            if job['state'] in ('failed', 'cancelled'):
                raise RuntimeError(f'unexpected job outcome: {job}')
            time.sleep(.025)
        raise RuntimeError('render deadline exceeded')

    def submit(self, snapshot, key='render-one', scale=1):
        return self.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': scale}, 202, {'Idempotency-Key': key})


def run_diagnostics(demo, d, put):
    """Exercise the opt-in structured diagnostic protocol end to end."""
    # Legacy clients keep the original envelope, including the same message.
    bad = sample(); bad['layers'][0]['transform']['scaleX'] = 0
    legacy = demo.call('PUT', '/v1/scenes/invalid', {'name': '无效几何', 'expectedRevision': 0, 'document': bad}, 400)
    assert set(legacy) == {'code', 'error'} and 'scale' in legacy['error'], legacy

    # Nested geometry rejection points back at the deep layer relationship.
    nested = sample()
    nested['layers'][2]['children'][0]['points'][0]['x'] = 500
    nested_diag = demo.diagnose('PUT', '/v1/scenes/nested', {'name': '嵌套无效', 'expectedRevision': 0, 'document': nested}, 400)
    assert nested_diag['protocolVersion'] == 2
    assert nested_diag['subject']['kind'] == 'document' and nested_diag['subject']['contentDigest']
    feature = nested_diag['diagnostics'][0]
    assert feature['code'] == 'polygon.bounds', feature
    assert feature['layer']['path'] == '/layers/2/0'
    assert feature['layer']['ancestors'][0]['id'] == 'inset'
    assert feature['field'] == 'points.0'
    nested_digest = nested_diag['subject']['contentDigest']

    # A render-budget rejection explains the combined layer cost, and no job
    # was accepted for that snapshot.
    demo.call('PUT', '/v1/assets/ribbon', tiny_png(), 201)
    heavy = sample(); heavy['width'] = 1500; heavy['height'] = 1500
    heavy['layers'] = [dict(heavy['layers'][0], id=f'sheet-{i}', width=1500, height=1500, transform={'scaleX': 1, 'scaleY': 1}) for i in range(20)]
    heavy['layers'].append({'id': 'stamp', 'kind': 'image', 'width': 60, 'height': 60, 'assetId': 'ribbon',
                            'opacity': 1, 'transform': {'x': 20, 'y': 30, 'scaleX': 1, 'scaleY': 1}})
    put(heavy, 1)
    current = demo.call('GET', '/v1/scenes/emblem')
    over = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision']}, 201)
    budget = demo.diagnose('POST', '/v1/jobs', {'snapshotId': over['id'], 'scale': 1}, 429,
                           {'Idempotency-Key': 'diagnose-budget'})
    assert budget['protocolVersion'] == 2
    assert budget['subject']['kind'] == 'snapshot' and budget['subject']['snapshotId'] == over['id']
    assert budget['subject']['snapshotDigest'] == over['digest'] and budget['subject']['scale'] == 1
    cost = budget['diagnostics'][0]
    assert cost['code'] == 'render.workBudget', cost
    assert cost['evidence']['cost'] > cost['evidence']['budget']
    assert len(cost['related']) >= 8 and cost['evidence']['contributions'][0]['pixels'] > 0
    contributions = cost['evidence']['contributions']
    assert all(c['layer']['path'].startswith('/layers/') for c in contributions)
    assert cost['evidence']['drawableLayers'] == 21 and cost['evidence']['shown'] == 8
    assert cost['evidence']['cost'] == 20 * 2250000 + 3600
    jobs = demo.call('GET', '/v1/status')['jobs']
    assert not jobs.get('queued') and not jobs.get('running'), jobs

    # Saving a new scene revision must not move the basis of a diagnostic for
    # the frozen snapshot that was checked before the edit.
    edited = sample(); edited['background'] = '#000000'
    put(edited, current['revision'])
    assert demo.call('GET', '/v1/snapshots/' + over['id'])['digest'] == over['digest']
    again = demo.diagnose('POST', '/v1/jobs', {'snapshotId': over['id'], 'scale': 1}, 429,
                          {'Idempotency-Key': 'diagnose-budget'})
    assert again['subject'] == budget['subject'], (again['subject'], budget['subject'])
    assert again['diagnostics'][0]['evidence'] == cost['evidence']

    # The same content is explained with the same basis whether it arrives by
    # direct save or through an exchange package. The package is a genuine
    # atlas-scene package whose digests match the invalid nested document.
    source = sample()
    source['layers'].append({'id': 'ribbon', 'kind': 'image', 'width': 60, 'height': 60, 'assetId': 'ribbon',
                             'opacity': 1, 'transform': {'x': 20, 'y': 30, 'scaleX': 1, 'scaleY': 1}})
    holder = demo.call('PUT', '/v1/scenes/asset-holder', {'name': '素材引用', 'expectedRevision': 0, 'document': source})
    pinned = demo.call('POST', '/v1/scenes/asset-holder/snapshots', {'expectedRevision': holder['revision']}, 201)
    package = demo.call('GET', '/v1/snapshots/' + pinned['id'] + '/package')
    assert package_digest(package['snapshot']) == package['digest'], 'canonical encoder drifted'
    assert snapshot_digest(package['snapshot']['document'], package['snapshot']['assets']) == package['snapshot']['digest']

    # Layer references to an unavailable asset are located in the submitted
    # document from both entry points, with the same content basis.
    demo.call('POST', '/v1/assets/ribbon/retire')
    saved_asset = demo.diagnose('PUT', '/v1/scenes/asset-copy', {'name': '直接保存', 'expectedRevision': 0, 'document': source}, 409)
    imported_asset = demo.diagnose('POST', '/v1/imports/copy-package', {'name': '包导入', 'package': package}, 409)
    assert saved_asset['diagnostics'][0]['code'] == 'asset.unavailable', saved_asset
    assert imported_asset['diagnostics'][0]['code'] == 'asset.unavailable', imported_asset
    assert imported_asset['diagnostics'][0]['evidence']['reason'] == 'retired'
    assert imported_asset['diagnostics'][0]['message'] != saved_asset['diagnostics'][0]['message']
    assert saved_asset['subject']['contentDigest'] == imported_asset['subject']['contentDigest']
    assert saved_asset['diagnostics'][0]['layer'] == imported_asset['diagnostics'][0]['layer']
    assert saved_asset['diagnostics'][0]['layer']['path'] == '/layers/3'
    assert saved_asset['diagnostics'][0]['evidence']['assetId'] == 'ribbon'

    forged = json.loads(json.dumps(package))
    forged['snapshot']['document'] = nested
    forged['snapshot']['digest'] = snapshot_digest(nested, {})
    forged['digest'] = package_digest(forged['snapshot'])
    imported = demo.diagnose('POST', '/v1/imports/from-package', {'name': '导入同内容', 'package': forged}, 400)
    direct = demo.diagnose('PUT', '/v1/scenes/direct-copy', {'name': '直接保存', 'expectedRevision': 0, 'document': nested}, 400)
    assert imported['subject']['origin'] == 'import' and direct['subject']['origin'] == 'save'
    assert imported['subject']['contentDigest'] == direct['subject']['contentDigest'] == nested_digest
    assert imported['diagnostics'] == direct['diagnostics'], 'save and import must agree'
    assert imported['diagnostics'][0]['layer']['path'] == '/layers/2/0'

    # A package-level failure keeps the package basis and never claims a
    # document was checked. Malformed JSON has no checked content at all, so it
    # stays in the legacy envelope even for an opt-in client.
    corrupt = json.loads(json.dumps(package))
    corrupt['snapshot']['document']['width'] += 1
    corrupt_diag = demo.diagnose('POST', '/v1/imports/corrupt', {'name': '损坏包', 'package': corrupt}, 400)
    assert corrupt_diag['diagnostics'][0]['code'] == 'package.digest', corrupt_diag
    assert corrupt_diag['subject']['kind'] == 'package'
    malformed = demo.call('PUT', '/v1/scenes/bad-json', b'not json', 400,
                          {'Accept': 'application/vnd.atlas.diagnostics.v2+json', 'Content-Type': 'application/json'})
    assert set(malformed) == {'code', 'error'}, malformed
    return {'workflow': 'diagnose-rejection', 'layerPath': feature['layer']['path'],
            'budgetCost': cost['evidence']['cost'], 'contentDigest': nested_digest}


def run(demo, workflow, out):
    d = sample()
    put = lambda doc, rev=0: demo.call('PUT', '/v1/scenes/emblem', {'name': '双色印记', 'expectedRevision': rev, 'document': doc})
    scene = put(d)
    if workflow == 'diagnose-rejection':
        return run_diagnostics(demo, d, put)
    if workflow == 'create-scene':
        assert demo.call('GET', '/v1/scenes/emblem')['document'] == scene['document']
        bad = sample(); bad['layers'][0]['transform']['scaleX'] = 0
        demo.call('PUT', '/v1/scenes/invalid', {'name': '无效几何', 'expectedRevision': 0, 'document': bad}, 400)
    elif workflow == 'revise-scene':
        d['background'] = '#F0F0F0'; assert put(d, 1)['revision'] == 2
        demo.call('PUT', '/v1/scenes/emblem', {'name': '旧版本', 'expectedRevision': 1, 'document': d}, 409)
    elif workflow == 'archive-scene':
        archived = demo.call('POST', '/v1/scenes/emblem/state', {'expectedRevision': 1, 'state': 'archived'})
        demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': archived['revision']}, 409)
        assert demo.call('POST', '/v1/scenes/emblem/state', {'expectedRevision': archived['revision'], 'state': 'active'})['revision'] == 3
    if workflow in ('import-asset', 'retire-asset'):
        asset = demo.call('PUT', '/v1/assets/ribbon', tiny_png(), 201)
        assert asset['width'] == 2 and asset['digest'] == hashlib.sha256(tiny_png()).hexdigest()
        demo.call('PUT', '/v1/assets/broken', b'not png', 400)
        layer = {'id': 'ribbon', 'kind': 'image', 'width': 60, 'height': 60, 'assetId': 'ribbon', 'opacity': 1, 'transform': {'x': 20, 'y': 30, 'scaleX': 1, 'scaleY': 1}}
        d['layers'].append(layer); put(d, 1)
        pinned = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': 2}, 201)
        demo.call('POST', '/v1/assets/ribbon/retire')
        demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': 2}, 409)
        result = demo.wait(demo.submit(pinned)['id']); assert result['state'] == 'succeeded'
        return {'workflow': workflow, 'assetDigest': asset['digest'], 'pinnedRender': result['state']}
    current = demo.call('GET', '/v1/scenes/emblem')
    snapshot = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision']}, 201)
    if workflow == 'freeze-scene':
        original_digest = snapshot['digest']; d['background'] = '#000000'; put(d, current['revision'])
        assert demo.call('GET', '/v1/snapshots/' + snapshot['id'])['digest'] == original_digest
    if workflow in ('export-package', 'import-package'):
        package = demo.call('GET', '/v1/snapshots/' + snapshot['id'] + '/package')
        restored = demo.call('POST', '/v1/imports/restored', {'name': '恢复的印记', 'package': package}, 201)
        assert restored['document'] == snapshot['document']
        corrupt = json.loads(json.dumps(package)); corrupt['snapshot']['document']['width'] += 1
        demo.call('POST', '/v1/imports/corrupt', {'name': '损坏包', 'package': corrupt}, 400)
        demo.call('GET', '/v1/scenes/corrupt', status=404)
    if workflow in ('cancel-render', 'retry-render', 'recover-render'):
        heavy = sample(); heavy['width'] = 1500; heavy['height'] = 1500
        heavy['layers'] = [dict(heavy['layers'][0], id=f'sheet-{i}', width=1500, height=1500, transform={'scaleX': 1, 'scaleY': 1}) for i in range(16)]
        put(heavy, current['revision'])
        snapshot = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision'] + 1}, 201)
    job = demo.submit(snapshot)
    if workflow == 'submit-render':
        assert demo.submit(snapshot)['id'] == job['id']
        demo.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': 2}, 409, {'Idempotency-Key': 'render-one'})
    if workflow == 'revoke-snapshot':
        demo.call('POST', '/v1/snapshots/' + snapshot['id'] + '/revoke')
        demo.call('POST', '/v1/jobs', {'snapshotId': snapshot['id'], 'scale': 1}, 409, {'Idempotency-Key': 'blocked-after-revoke'})
    if workflow in ('cancel-render', 'retry-render'):
        stopped = demo.call('POST', '/v1/jobs/' + job['id'] + '/cancel')
        assert stopped['state'] == 'cancelled'
        if workflow == 'cancel-render':
            time.sleep(.1); assert demo.call('GET', '/v1/jobs/' + job['id'])['state'] == 'cancelled'
            return {'workflow': workflow, 'state': 'cancelled'}
        job = demo.call('POST', '/v1/jobs/' + job['id'] + '/retry', status=202)
        assert job['attempt'] == 2
    if workflow == 'recover-render':
        demo.wait(job['id'], wanted=('running',)); demo.stop(abrupt=True); demo.start()
    result = demo.wait(job['id'])
    png = demo.call('GET', '/v1/artifacts/' + result['artifactId'])
    assert png[:8] == b'\x89PNG\r\n\x1a\n'
    assert struct.unpack('>II', png[16:24]) == (snapshot['document']['width'], snapshot['document']['height'])
    if out:
        Path(out).mkdir(parents=True, exist_ok=True); (Path(out) / 'atlas-sample.png').write_bytes(png)
    if workflow == 'compare-renders':
        other = demo.wait(demo.submit(snapshot, 'render-two')['id'])
        comparison = demo.call('POST', '/v1/compare', {'left': result['artifactId'], 'right': other['artifactId']})
        assert comparison['changedPixels'] == 0
        d['background'] = '#000000'; put(d, current['revision'])
        altered = demo.call('POST', '/v1/scenes/emblem/snapshots', {'expectedRevision': current['revision'] + 1}, 201)
        changed = demo.wait(demo.submit(altered, 'render-three')['id'])
        assert demo.call('POST', '/v1/compare', {'left': result['artifactId'], 'right': changed['artifactId']})['changedPixels'] > 0
    if workflow == 'purge-artifact':
        demo.call('DELETE', '/v1/artifacts/' + result['artifactId'])
        demo.call('GET', '/v1/artifacts/' + result['artifactId'], status=410)
        assert demo.call('GET', '/v1/jobs/' + job['id'])['state'] == 'succeeded'
    if workflow == 'observe-service':
        summary = demo.call('GET', '/v1/status'); assert summary['jobs']['succeeded'] >= 1
        events = demo.call('GET', '/v1/events'); assert any(e['kind'] == 'job.succeeded' for e in events)
    return {'workflow': workflow, 'state': result['state'], 'pngBytes': len(png), 'pngDigest': hashlib.sha256(png).hexdigest()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--workflow', default='submit-render')
    parser.add_argument('--out')
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='atlas-walkthrough-') as temp:
        directory = Path(temp); binary = directory / 'atlas'
        subprocess.run(['go', 'build', '-o', str(binary), './cmd/atlas'], cwd=ROOT, check=True, timeout=60)
        demo = Demo(binary, directory)
        try:
            print(json.dumps(run(demo, args.workflow, args.out), ensure_ascii=False))
        finally:
            demo.stop(); demo.log.close()


if __name__ == '__main__':
    main()
