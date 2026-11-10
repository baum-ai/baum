package livesessions

import "testing"

func TestImageCacheReleasesEvictedBuffers(t *testing.T) {
	// A backing array with room to spare, so the puts never move the
	// entries and its first slots stay visible after eviction.
	backing := make([]recentImage, 0, 2*recentImageCount)
	c := imageCache{entries: backing}
	for n := 0; n <= recentImageCount; n++ {
		c.put(imageKey{path: "/t.jsonl", ref: "r", n: n}, "image/png", []byte("x"))
	}
	if len(c.entries) != recentImageCount {
		t.Fatalf("%d entries kept, want %d", len(c.entries), recentImageCount)
	}
	if evicted := backing[:1][0]; evicted.data != nil || evicted.key != (imageKey{}) {
		t.Fatalf("the evicted entry is still held by the cache's backing array: %+v", evicted)
	}
}

func TestImageCacheKeepsAFewImagesAndDropsStaleOnes(t *testing.T) {
	var c imageCache
	key := func(n int, size int64) imageKey { return imageKey{path: "/t.jsonl", ref: "r", n: n, size: size} }

	c.put(key(0, 10), "image/png", []byte("a"))
	if _, data, ok := c.get(key(0, 10)); !ok || string(data) != "a" {
		t.Fatalf("get = %q, %v, want the image put", data, ok)
	}
	// The transcript grew: the old entry is not served and is replaced.
	if _, _, ok := c.get(key(0, 11)); ok {
		t.Fatal("an image of another file size was served")
	}
	c.put(key(0, 11), "image/png", []byte("b"))
	if _, _, ok := c.get(key(0, 10)); ok {
		t.Fatal("the stale entry was kept")
	}
	for n := 1; n <= recentImageCount+2; n++ {
		c.put(key(n, 11), "image/png", []byte("x"))
	}
	if len(c.entries) != recentImageCount {
		t.Fatalf("%d entries kept, want %d", len(c.entries), recentImageCount)
	}
	if _, _, ok := c.get(key(0, 11)); ok {
		t.Fatal("the oldest entry survived")
	}
}
