package domain

// AlbumSubscriberThresholds задаёт число подписчиков для каждого следующего места в подборке.
func AlbumSubscriberThresholds() []int64 {
	return []int64{500_000, 1_000_000, 3_000_000, 5_000_000}
}

// AlbumImportLimit возвращает лимит альбомов по подписчикам YouTube Music.
func AlbumImportLimit(subscribers *int64) int {
	if subscribers == nil {
		return 1
	}
	limit := 1
	for _, threshold := range AlbumSubscriberThresholds() {
		if *subscribers < threshold {
			break
		}
		limit++
	}
	return limit
}
