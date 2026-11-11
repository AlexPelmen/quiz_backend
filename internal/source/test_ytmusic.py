import importlib.util
import pathlib
import sys
import types
import unittest
from unittest.mock import patch


ytmusicapi = types.ModuleType("ytmusicapi")


class FakeYTMusic:
    def get_artist(self, channel_id):
        return self._send_request("browse", {"browseId": channel_id})

    def _send_request(self, endpoint, body):
        return self.response


ytmusicapi.YTMusic = FakeYTMusic
exceptions = types.ModuleType("ytmusicapi.exceptions")
exceptions.YTMusicUserError = type("YTMusicUserError", (Exception,), {})
with patch.dict(sys.modules, {"ytmusicapi": ytmusicapi, "ytmusicapi.exceptions": exceptions}):
    spec = importlib.util.spec_from_file_location("quiz_ytmusic", pathlib.Path(__file__).with_name("ytmusic.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)


class ArtistSearchTest(unittest.TestCase):
    def test_resolve_artist_url_requires_artist_page(self):
        class Client:
            def _send_request(self, endpoint, body):
                self.request = endpoint, body
                return {"endpoint": {"browseEndpoint": {"browseId": "UCNEStAEX0fKYbZIXTAW8Juw",
                        "browseEndpointContextSupportedConfigs": {"browseEndpointContextMusicConfig": {
                            "pageType": self.page_type}}}}}

        client = Client()
        client.page_type = "MUSIC_PAGE_TYPE_ARTIST"
        lookup = {"operation": "resolve_artist_url", "key": "https://music.youtube.com/@avantasiaofficial", "name": "Avantasia"}
        self.assertEqual(module.parse_request(client, lookup), {"yt_music_id": "UCNEStAEX0fKYbZIXTAW8Juw"})
        client.page_type = "MUSIC_PAGE_TYPE_ALBUM"
        with self.assertRaises(ValueError):
            module.parse_request(client, lookup)

    def test_search_uses_first_result(self):
        class Client:
            def search(self, name, filter):
                return [{"artist": "Different", "browseId": "first"}, {"artist": name, "browseId": "second"}]
        self.assertEqual(module.parse_request(Client(), {"operation": "search_artist", "name": "Artist"}), {"yt_music_id": "first"})

    def test_monthly_listeners_fallback(self):
        self.assertEqual(module.monthly_listeners({"subscribers": "1.5K"}), 3000)
        self.assertEqual(module.monthly_listeners({"subscribers": "1.5K", "monthlyListeners": "42"}), 42)
        self.assertIsNone(module.monthly_listeners({}))


class ArtistCarouselTest(unittest.TestCase):
    def test_only_artist_response_discards_headerless_carousel(self):
        bad = {"musicCarouselShelfRenderer": {"contents": []}}
        good = {"musicCarouselShelfRenderer": {"header": {}, "contents": []}}
        client = module.MusicClient()
        client.response = {"contents": [bad, good, {"musicShelfRenderer": {}}]}

        self.assertEqual(client.get_artist("candidate")["contents"], [good, {"musicShelfRenderer": {}}])
        client.response = {"contents": [bad]}
        self.assertEqual(client._send_request("browse", {})["contents"], [bad])

    def test_artist_flag_is_cleared_after_error(self):
        client = module.MusicClient()
        with patch.object(FakeYTMusic, "get_artist", side_effect=KeyError("other")):
            with self.assertRaises(KeyError):
                client.get_artist("candidate")
        self.assertFalse(client._reading_artist)


class RankedSongsTest(unittest.TestCase):
    def test_counts_are_read_from_playlist_columns(self):
        response = {"musicResponsiveListItemRenderer": {
            "playlistItemData": {"videoId": "holiday"},
            "flexColumns": [{"musicResponsiveListItemFlexColumnRenderer": {"text": {
                "runs": [{"text": "Turnstile"}, {"text": " • "}, {"text": "11M plays"}]}}}],
            "menu": {"text": "99M plays"},
        }}
        counts = {}
        module.playlist_play_counts(response, counts)
        self.assertEqual(counts, {"holiday": 11000000})

    def test_first_twenty_are_ranked_by_music_plays(self):
        class Client:
            def get_playlist(self, key, limit):
                self.limit = limit
                return {"tracks": [{"videoId": str(i), "title": str(i), "plays": i} for i in range(21)]}
        client = Client()
        result = module.parse_request(client, {"operation": "playlist", "key": "id", "name": "artist"})
        self.assertEqual(client.limit, 20)
        self.assertEqual([row["yt_plays"] for row in result["tracks"][:3]], [19, 18, 17])
        self.assertEqual(len(result["tracks"]), 20)

    def test_unknown_counts_are_not_ranked(self):
        class Client:
            def get_playlist(self, key, limit):
                return {"tracks": [{"videoId": "id", "title": "song"}]}
        with self.assertRaises(ValueError):
            module.parse_request(Client(), {"operation": "playlist", "key": "id", "name": "artist"})


if __name__ == "__main__":
    unittest.main()
