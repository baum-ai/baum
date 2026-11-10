package livesessions

import "testing"

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
