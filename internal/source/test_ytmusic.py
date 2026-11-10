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

    def test_search_accepts_transliteration_and_formatting(self):
        for query, result in [("Сектор Газа", "Sektor Gaza"), ("Алиса", "Alisa"),
                              ("ДДТ", "DDT"), ("Trust Company", "TRUSTcompany"),
                              ("Joan Jett and the Blackhearts", "Joan Jett & The Blackhearts")]:
            with self.subTest(query=query):
                rows = [{"artist": result, "browseId": "match"}]
                self.assertEqual(module.artist_candidates(rows, query), ["match"])

    def test_exact_match_has_priority_and_ambiguous_aliases_are_preserved(self):
        rows = [{"artist": "Kino", "browseId": "one"}, {"artist": "KINO", "browseId": "two"},
                {"artist": "Кино", "browseId": "exact"}]
        self.assertEqual(module.artist_candidates(rows, "Кино"), ["exact"])
        self.assertEqual(module.artist_candidates(rows[:2], "Кино"), ["one", "two"])
        self.assertEqual(module.artist_candidates([{"artist": "Сектор Газа EX", "browseId": "other"}],
                                                  "Сектор Газа"), [])


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


if __name__ == "__main__":
    unittest.main()
