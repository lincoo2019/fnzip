import hashlib
import io
import os
import sys
import tarfile


def read_entries(tf):
    result = []
    for m in tf.getmembers():
        data = tf.extractfile(m).read() if m.isfile() else None
        result.append((m.name, m.isdir(), data))
    return result


def main():
    if len(sys.argv) != 3:
        print('usage: fix_fpk.py <input.fpk> <output.fpk>')
        return 2
    src, dst = sys.argv[1], sys.argv[2]

    with tarfile.open(src, 'r:gz') as tf:
        outer = read_entries(tf)

    app_tgz = next((data for name, isdir, data in outer if name == 'app.tgz'), None)
    if app_tgz is None:
        print('app.tgz not found in fpk')
        return 1

    with tarfile.open(fileobj=io.BytesIO(app_tgz), mode='r:gz') as inner:
        app_entries = read_entries(inner)

    inner_buf = io.BytesIO()
    with tarfile.open(fileobj=inner_buf, mode='w:gz', format=tarfile.GNU_FORMAT) as out:
        for name, isdir, data in app_entries:
            ti = tarfile.TarInfo(name)
            ti.uid = ti.gid = 0
            ti.uname = ti.gname = 'root'
            ti.mtime = 0
            if isdir:
                ti.type = tarfile.DIRTYPE
                ti.mode = 0o755
                ti.size = 0
                out.addfile(ti)
            else:
                ti.type = tarfile.REGTYPE
                ti.mode = 0o755 if name.startswith('bin/') else 0o644
                ti.size = len(data)
                out.addfile(ti, io.BytesIO(data))
    new_app_tgz = inner_buf.getvalue()
    checksum = hashlib.md5(new_app_tgz).hexdigest()

    manifest = None
    for name, isdir, data in outer:
        if name == 'manifest' and data is not None:
            manifest = data
    if manifest is None:
        print('manifest not found')
        return 1
    lines = [line for line in manifest.decode('utf-8').splitlines()
             if not line.strip().startswith('checksum')]
    lines.append('checksum              = ' + checksum)
    new_manifest = ('\n'.join(lines) + '\n').encode('utf-8')

    os.makedirs(os.path.dirname(os.path.abspath(dst)), exist_ok=True)
    with tarfile.open(dst, 'w:gz', format=tarfile.GNU_FORMAT) as out:
        for name, isdir, data in outer:
            if name == 'app.tgz':
                data = new_app_tgz
            elif name == 'manifest':
                data = new_manifest
            ti = tarfile.TarInfo(name)
            ti.uid = ti.gid = 0
            ti.uname = ti.gname = 'root'
            ti.mtime = 0
            if isdir:
                ti.type = tarfile.DIRTYPE
                ti.mode = 0o755
                ti.size = 0
                out.addfile(ti)
            else:
                ti.type = tarfile.REGTYPE
                ti.mode = 0o755 if name.startswith('cmd/') else 0o644
                ti.size = len(data)
                out.addfile(ti, io.BytesIO(data))

    print('checksum =', checksum)
    print('wrote', dst)
    return 0


if __name__ == '__main__':
    sys.exit(main())
