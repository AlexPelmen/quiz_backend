"""Проверка и перенос файлов локального docker compose Music Quiz."""
import json
import datetime
import hashlib
import os
import pathlib
import re
import shutil
import subprocess
import sys
import unicodedata
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[2]
MEDIA = (ROOT / "media").resolve()
QUERY = r"""
SELECT COALESCE(json_agg(row_to_json(x)),'[]'::json) FROM (
 SELECT d.id, d.uri, d.preview,
 COALESCE((SELECT a.name FROM track_to_download td JOIN artist_to_track own ON own.track_id=td.track_id AND own.is_primary JOIN artists a ON a.id=own.artist_id WHERE td.download_id=d.id ORDER BY td.track_id LIMIT 1),
 (SELECT a.name FROM artists a WHERE a.logo_download_id=d.id ORDER BY a.id LIMIT 1),
 (SELECT a.name FROM albums al JOIN artist_to_album own ON own.album_id=al.id JOIN artists a ON a.id=own.artist_id WHERE al.cover_download_id=d.id ORDER BY al.id LIMIT 1),'') AS artist,
 COALESCE((SELECT t.name FROM track_to_download td JOIN tracks t ON t.id=td.track_id WHERE td.download_id=d.id ORDER BY td.track_id LIMIT 1),
 (SELECT al.name FROM albums al WHERE al.cover_download_id=d.id ORDER BY al.id LIMIT 1),'') AS title
 FROM downloads d WHERE d.uri<>'' AND d.status='finished'
) x
"""


def name_for(artist, title, preview):
    def clean(text):
        text = ''.join('_' if unicodedata.category(c) == 'Cc' or c in '<>:"/\\|?*' else c for c in text)
        return text.strip().strip('. ')
    artist, title = clean(artist), clean(title)
    if preview and title:
        title = 'превью ' + title
    name = (artist + ' - ' + title) if artist and title else (artist or title)
    name = name[:120].rstrip('. ')
    if re.fullmatch(r'(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])', name, re.IGNORECASE):
        name = '_' + name
    return name


def plan():
    result = subprocess.run(['docker', 'compose', 'exec', '-T', 'db', 'psql', '-U', 'quiz', '-d', 'quiz', '-X', '-A', '-t', '-c', QUERY],
                            cwd=ROOT, check=True, stdout=subprocess.PIPE, encoding='utf-8')
    rows = json.loads(result.stdout)
    missing, unnamed, already, candidates = [], [], [], []
    targets = set()
    for row in rows:
        uri = row['uri']
        if not uri.startswith('/app/data/'):
            missing.append(row['id'])
            continue
        rel = pathlib.PurePosixPath(uri).relative_to('/app/data')
        unresolved = MEDIA / pathlib.Path(*rel.parts)
        source = unresolved.resolve()
        if not source.is_relative_to(MEDIA) or not source.is_file() or any(p.is_symlink() for p in [unresolved, *unresolved.parents] if p != MEDIA.parent):
            missing.append(row['id'])
            continue
        name = name_for(row['artist'], row['title'], row['preview'])
        if not name:
            unnamed.append(row['id'])
            continue
        target = source.with_name(name + source.suffix.lower())
        if source == target:
            already.append(row['id'])
            continue
        if target.exists() or str(target).casefold() in targets:
            target = source.with_name(name + ' - ' + row['id'] + source.suffix.lower())
        if target.exists() or str(target).casefold() in targets:
            raise RuntimeError('Повторное совпадение целевого имени: ' + str(target))
        targets.add(str(target).casefold())
        candidates.append({'id': row['id'], 'old': uri, 'new': '/app/data/' + target.relative_to(MEDIA).as_posix(), 'bytes': source.stat().st_size})
    print(json.dumps({'rename': len(candidates), 'bytes': sum(row['bytes'] for row in candidates),
                      'already_named': len(already), 'missing': len(missing), 'without_catalog_name': len(unnamed)}, ensure_ascii=False))
    if missing:
        raise RuntimeError('Обнаружены отсутствующие или недопустимые файлы; перенос отменён')
    return candidates


def docker(*args, **kwargs):
    return subprocess.run(['docker', 'compose', *args], cwd=ROOT, check=True, **kwargs)


def host_path(uri):
    relative = pathlib.PurePosixPath(uri).relative_to('/app/data')
    return MEDIA.joinpath(*relative.parts)


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(4 * 1024 * 1024), b''):
            h.update(block)
    return h.digest()


def commit_and_remove(rows):
    def quote(value):
        return "'" + value.replace("'", "''") + "'"
    values = ',\n'.join('(' + ','.join(map(quote, (row['id'], row['old'], row['new']))) + ')' for row in rows)
    if rows:
        tag = '$migration_' + uuid.uuid4().hex + '$'
        sql = f'''BEGIN;
DO {tag} DECLARE changed integer; BEGIN
UPDATE downloads d SET uri=v.new_uri,updated_at=now() FROM (VALUES {values}) AS v(id,old_uri,new_uri)
WHERE d.id=v.id::uuid AND d.uri=v.old_uri;
GET DIAGNOSTICS changed = ROW_COUNT;
IF changed <> {len(rows)} THEN RAISE EXCEPTION 'Количество обновлений не совпало: %', changed; END IF;
END {tag};
COMMIT;'''
        docker('exec', '-T', 'db', 'psql', '-U', 'quiz', '-d', 'quiz', '-X', '-v', 'ON_ERROR_STOP=1', input=sql, text=True, encoding='utf-8')
    failures = []
    for row in rows:
        try:
            host_path(row['old']).unlink()
        except OSError as err:
            failures.append(str(err))
    print(json.dumps({'updated': len(rows), 'remove_errors': failures}, ensure_ascii=False), flush=True)
    if failures:
        raise RuntimeError('Не удалось удалить некоторые старые копии')


def resume(manifest):
    rows = json.loads(pathlib.Path(manifest).read_text(encoding='utf-8'))
    docker('stop', 'api', stdout=subprocess.DEVNULL)
    try:
        for index, row in enumerate(rows, 1):
            source, target = host_path(row['old']), host_path(row['new'])
            if (not source.resolve().is_relative_to(MEDIA) or not target.resolve().is_relative_to(MEDIA)
                    or source.is_symlink() or target.is_symlink() or not source.is_file() or not target.is_file()
                    or source.stat().st_size != row['bytes'] or target.stat().st_size != row['bytes']
                    or digest(source) != digest(target)):
                raise RuntimeError('Проверка подготовленной копии не пройдена: ' + str(source))
            if index % 250 == 0:
                print(f'Проверено {index} из {len(rows)}', flush=True)
        commit_and_remove(rows)
    finally:
        docker('start', 'api')


def verify(manifest):
    rows = json.loads(pathlib.Path(manifest).read_text(encoding='utf-8'))
    query = "SELECT COALESCE(json_agg(row_to_json(x)),'[]'::json) FROM (SELECT id,uri FROM downloads WHERE uri<>'') x"
    result = docker('exec', '-T', 'db', 'psql', '-U', 'quiz', '-d', 'quiz', '-X', '-A', '-t', '-c', query,
                    stdout=subprocess.PIPE, encoding='utf-8')
    current = {row['id']: row['uri'] for row in json.loads(result.stdout)}
    bad = [row['id'] for row in rows if current.get(row['id']) != row['new']
           or not host_path(row['new']).is_file() or host_path(row['old']).exists()]
    print(json.dumps({'checked': len(rows), 'errors': len(bad)}, ensure_ascii=False))
    if bad:
        raise RuntimeError('Некоторые записи или файлы не прошли проверку')


def apply():
    container = docker('ps', '-q', 'api', stdout=subprocess.PIPE, text=True).stdout.strip()
    if not container:
        raise RuntimeError('API не запущен')
    mounts = json.loads(subprocess.run(['docker', 'inspect', container], check=True, stdout=subprocess.PIPE, text=True).stdout)[0]['Mounts']
    mount = next((m for m in mounts if m['Destination'] == '/app/data'), None)
    if not mount or pathlib.Path(mount['Source']).resolve() != MEDIA:
        raise RuntimeError('Подключённое хранилище не совпадает с папкой media')
    docker('stop', 'api', stdout=subprocess.DEVNULL)
    try:
        rows = plan()
        backups = ROOT / 'backups'
        backups.mkdir(exist_ok=True)
        stamp = datetime.datetime.now().strftime('%Y%m%d-%H%M%S') + '-' + uuid.uuid4().hex[:8]
        manifest = backups / ('media-rename-' + stamp + '.json')
        with manifest.open('x', encoding='utf-8') as f:
            json.dump(rows, f, ensure_ascii=False, indent=2)
        dump = backups / ('media-rename-' + stamp + '.dump')
        with dump.open('xb') as f:
            docker('exec', '-T', 'db', 'pg_dump', '-U', 'quiz', '-d', 'quiz', '-Fc', stdout=f)
        if not dump.stat().st_size:
            raise RuntimeError('Резервная копия базы пуста')
        if shutil.disk_usage(MEDIA).free < sum(row['bytes'] for row in rows) + 256 * 1024 * 1024:
            raise RuntimeError('Недостаточно места для безопасного копирования')
        for index, row in enumerate(rows, 1):
            source, target = host_path(row['old']), host_path(row['new'])
            before = source.stat()
            if before.st_size != row['bytes'] or not source.resolve().is_relative_to(MEDIA) or source.is_symlink():
                raise RuntimeError('Исходный файл изменился: ' + str(source))
            with source.open('rb') as src, target.open('xb') as dst:
                shutil.copyfileobj(src, dst, 4 * 1024 * 1024)
                dst.flush()
                os.fsync(dst.fileno())
            after = source.stat()
            if before.st_size != after.st_size or before.st_mtime_ns != after.st_mtime_ns or digest(source) != digest(target):
                raise RuntimeError('Проверка копии не пройдена: ' + str(source))
            if index % 100 == 0:
                print(f'Подготовлено {index} из {len(rows)}', flush=True)
        commit_and_remove(rows)
        print(json.dumps({'manifest': str(manifest), 'backup': str(dump)}, ensure_ascii=False), flush=True)
    finally:
        docker('start', 'api')


def main():
    if sys.argv[1:] == []:
        plan()
    elif sys.argv[1:] == ['--apply']:
        apply()
    elif len(sys.argv) == 3 and sys.argv[1] == '--resume':
        resume(sys.argv[2])
    elif len(sys.argv) == 3 and sys.argv[1] == '--verify':
        verify(sys.argv[2])
    else:
        raise SystemExit('Использование: media_rename.py [--apply]')


if __name__ == '__main__':
    main()
