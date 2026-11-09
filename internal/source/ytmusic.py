import json
import re
import sys
import time
import unicodedata
from decimal import Decimal

from ytmusicapi import YTMusic
from ytmusicapi.exceptions import YTMusicUserError


def retry(call, *args, **kwargs):
    for delay in (1, 2, 3):
        try:
            return call(*args, **kwargs)
        except YTMusicUserError:
            raise
        except Exception:
            time.sleep(delay)
    return call(*args, **kwargs)


def normalized(text):
    return " ".join(unicodedata.normalize("NFKC", text).casefold().split())


def count(value):
    if value is None:
        return None
    match = re.fullmatch(r"([0-9]+(?:\.[0-9]+)?)\s*([KMB])?(?:\s+(?:plays?|views?|subscribers?|monthly listeners?))?",
                         str(value).replace(",", "").strip(), re.IGNORECASE)
    if not match:
        return None
    scale = {"": 1, "K": 1000, "M": 1000000, "B": 1000000000}
    number = int(Decimal(match[1]) * scale[(match[2] or "").upper()])
    return number if number <= 2**63 - 1 else None


def thumbnail(row):
    images = row.get("thumbnails") or row.get("thumbnail") or []
    return max(images, key=lambda image: image.get("width") or 0, default={}).get("url", "")


def duration(row):
    seconds = count(row.get("duration_seconds") or row.get("lengthSeconds"))
    if seconds:
        return seconds
    text = str(row.get("duration") or "")
    if re.fullmatch(r"\d+(?::\d{1,2}){1,2}", text):
        seconds = 0
        for part in text.split(":"):
            seconds = seconds * 60 + int(part)
        return seconds or None
    return None


def limit_for(subs):
    return 1 + sum((subs or 0) >= boundary for boundary in
                   (100_000, 300_000, 500_000, 1_000_000, 2_000_000, 5_000_000, 10_000_000))


def plays_for(row):
    for field in ("views", "viewCount", "playCount", "plays"):
        plays = count(row.get(field))
        if plays is not None:
            return plays
    return None


def fill_song(yt, track, warnings):
    video = track["yt_music_id"]
    if track.get("yt_plays") is not None and track.get("duration_seconds") is not None:
        return
    try:
        details = retry(yt.get_song, video).get("videoDetails") or {}
        if track.get("yt_plays") is None:
            track["yt_plays"] = plays_for(details)
        if track.get("duration_seconds") is None:
            track["duration_seconds"] = duration(details)
    except Exception as exc:
        warnings.append(f"YouTube Music song {video}: {type(exc).__name__}")


def album_name(row):
    album = row.get("album")
    if isinstance(album, dict):
        return album.get("name") or ""
    return album or ""


def resolve_songs(yt, lookup):
    catalog = {"tracks": [], "warnings": []}
    artist_name = lookup["name"]
    for track in lookup["tracks"]:
        try:
            if not track.get("yt_music_id"):
                query = f'{artist_name} {track["title"]}'
                rows = retry(yt.search, query, filter="songs", limit=8)
                matches = {}
                for row in rows:
                    if not row.get("videoId") or normalized(row.get("title") or "") != normalized(track["title"]):
                        continue
                    if not any(normalized(a.get("name") or "") == normalized(artist_name)
                               or lookup.get("artist_id") and a.get("id") == lookup["artist_id"]
                               for a in (row.get("artists") or [])):
                        continue
                    expected = track.get("duration_seconds")
                    actual = duration(row)
                    if expected and actual and abs(expected - actual) > 1:
                        continue
                    matches[row["videoId"]] = row
                if len(matches) > 1 and album_name(track):
                    album = normalized(album_name(track))
                    matches = {video: row for video, row in matches.items()
                               if normalized(album_name(row)) == album}
                if len(matches) != 1:
                    catalog["warnings"].append(f'YouTube Music: no unique match for {track["title"]}')
                    continue
                video, row = next(iter(matches.items()))
                track["yt_music_id"] = video
                if track.get("yt_plays") is None:
                    track["yt_plays"] = plays_for(row)
                if track.get("duration_seconds") is None:
                    track["duration_seconds"] = duration(row)
            fill_song(yt, track, catalog["warnings"])
            catalog["tracks"].append(track)
        except Exception as exc:
            catalog["warnings"].append(f'YouTube Music search {track["title"]}: {type(exc).__name__}')
    return catalog


def load_album_plays(yt, lookup):
    catalog = {"albums": [], "warnings": []}
    top_songs = {song["yt_music_id"]: song.get("yt_plays") for song in lookup.get("top_songs") or []}
    playlist = lookup.get("top_songs_playlist")
    for album in lookup["albums"]:
        album_id = album["yt_music_id"]
        try:
            tracks = retry(yt.get_album, album_id).get("tracks") or []
            counts = [plays for track in tracks if (plays := plays_for(track)) is not None]
            if counts:
                catalog["albums"].append({"yt_music_id": album_id, "yt_plays": max(counts)})
                continue

            videos = {track.get("videoId") for track in tracks if track.get("videoId")}
            video = next((video for video in top_songs if video in videos), None)
            if video is None and videos and playlist:
                try:
                    rows = retry(yt.get_playlist, playlist, limit=100).get("tracks") or []
                    for row in rows:
                        video_id = row.get("videoId")
                        if video_id and top_songs.get(video_id) is None:
                            top_songs[video_id] = plays_for(row)
                except Exception as exc:
                    catalog["warnings"].append(f"YouTube Music top songs: {type(exc).__name__}")
                playlist = None
                video = next((video for video in top_songs if video in videos), None)

            plays = top_songs.get(video)
            if video and plays is None:
                details = retry(yt.get_song, video).get("videoDetails") or {}
                plays = plays_for(details)
                top_songs[video] = plays
            if plays is None:
                reason = "play count unavailable" if video else "no album track in the artist's top songs"
                catalog["warnings"].append(f"YouTube Music album {album_id}: {reason}")
            else:
                catalog["albums"].append({"yt_music_id": album_id, "yt_plays": plays})
        except Exception as exc:
            catalog["warnings"].append(f"YouTube Music album {album_id}: {type(exc).__name__}")
    return catalog


def load_catalog(yt, lookup):
    name = lookup["name"]
    artist_id = lookup.get("artist_id")
    if not artist_id:
        matches = {item["browseId"] for item in retry(yt.search, name, filter="artists")
                   if item.get("browseId") and normalized(item.get("artist") or "") == normalized(name)}
        if len(matches) != 1:
            raise ValueError("artist not found unambiguously")
        artist_id = matches.pop()
    artist = retry(yt.get_artist, artist_id)
    subs = count(artist.get("subscribers"))
    plays = count(artist.get("monthlyListeners"))
    if plays is None:
        plays = count(artist.get("monthlyListenerCount"))
    catalog = {"yt_music_id": artist_id, "name": artist.get("name") or name, "logo_url": thumbnail(artist),
               "yt_subs": subs, "yt_plays": plays, "albums": [], "tracks": [], "warnings": []}

    songs = artist.get("songs") or {}
    rows = songs.get("results") or []
    playlist = (songs.get("browseId") or "").removeprefix("VL")
    selected = set()
    known_subs = lookup.get("yt_subs")
    use_subs = known_subs if lookup.get("keep_subs") or subs is None else subs
    limit = limit_for(use_subs)
    if len({row.get("videoId") for row in rows if row.get("videoId") and row.get("title")}) < limit and playlist:
        try:
            extra = retry(yt.get_playlist, playlist, limit=limit).get("tracks") or []
            rows = rows + extra
        except Exception as exc:
            catalog["warnings"].append(f"YouTube Music top songs: {type(exc).__name__}")
    top_songs = {}
    for row in rows:
        if row.get("videoId"):
            top_songs.setdefault(row["videoId"], {"yt_music_id": row["videoId"], "yt_plays": plays_for(row)})
    catalog["top_songs"] = list(top_songs.values())
    catalog["top_songs_playlist"] = playlist
    cached = {row["yt_music_id"]: row for row in lookup.get("saved_tracks") or [] if row.get("yt_music_id")}
    for row in rows:
        video = row.get("videoId")
        title = row.get("title")
        if not video or not title or video in selected:
            continue
        selected.add(video)
        release = row.get("album") or {}
        track = {"yt_music_id": video, "title": title, "yt_plays": plays_for(row),
                 "duration_seconds": duration(row)}
        saved = cached.get(video, {})
        for field in ("yt_plays", "duration_seconds"):
            if saved.get(field) is not None:
                track[field] = saved[field]
        fill_song(yt, track, catalog["warnings"])
        if saved.get("yt_plays") is None:
            top_songs[video]["yt_plays"] = track["yt_plays"]
        if isinstance(release, dict) and release.get("name"):
            track["album"] = {"yt_music_id": release.get("id"), "name": release["name"]}
        elif isinstance(release, str) and release:
            track["album"] = {"name": release}
        catalog["tracks"].append(track)
        if len(selected) >= limit:
            break

    albums = artist.get("albums") or {}
    rows = albums.get("results") or []
    if albums.get("browseId") and albums.get("params"):
        try:
            rows = retry(yt.get_artist_albums, albums["browseId"], albums["params"], limit=20, order="Popularity")
            catalog["albums_ranked"] = True
        except Exception as exc:
            catalog["warnings"].append(f"YouTube Music top albums: {type(exc).__name__}")
    for row in rows[:20]:
        album_id = row.get("browseId")
        if album_id and row.get("title"):
            catalog["albums"].append({"yt_music_id": album_id, "name": row["title"], "type": row.get("type") or "",
                                      "cover_art_url": thumbnail(row), "release_date": str(row.get("year") or "")})
    return catalog


def music_client():
    from requests import Session

    class PacedSession(Session):
        next_request = 0.0

        def send(self, request, **kwargs):
            time.sleep(max(0, self.next_request - time.monotonic()))
            self.next_request = time.monotonic() + 1
            if kwargs.get("timeout") is None:
                kwargs["timeout"] = 10
            return super().send(request, **kwargs)

    return YTMusic(language="en", requests_session=PacedSession())


if __name__ == "__main__":
    try:
        lookup = json.load(sys.stdin)
        yt = music_client()
        if lookup.get("albums"):
            result = load_album_plays(yt, lookup)
        elif lookup.get("tracks"):
            result = resolve_songs(yt, lookup)
        else:
            result = load_catalog(yt, lookup)
        print(json.dumps(result, ensure_ascii=True))
    except Exception as exc:
        sys.exit(f"YouTube Music: {type(exc).__name__}: {exc}")
