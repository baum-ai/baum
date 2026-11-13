package tileview

import (
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/etagjson"
)

func body(s string) etagjson.Body { return etagjson.Body{Data: []byte(s), ETag: `"` + s + `"`} }

func TestCacheDropsTheLeastRecentlyUsedAndKeepsNothingReachable(t *testing.T) {
	var c Cache
	for i := range 10 * maxAnswers {
		c.Put(string(rune('A'+i)), body("x"))
	}
	if len(c.answers) != maxAnswers || cap(c.answers) > 2*maxAnswers+2 {
		t.Fatalf("%d answers, capacity %d", len(c.answers), cap(c.answers))
	}
	// Nothing past the length keeps a dropped body.
	for _, a := range c.answers[:cap(c.answers)][len(c.answers):] {
		if a.key != "" || a.body.Data != nil {
			t.Fatalf("a dropped answer is still held: %+v", a)
		}
	}
	if _, ok := c.Get("A"); ok {
		t.Error("the oldest answer was kept")
	}
	if _, ok := c.Get(string(rune('A' + 10*maxAnswers - 1))); !ok {
		t.Error("the newest answer was dropped")
	}
	if _, ok := c.Get(""); ok {
		t.Error("an empty key was kept")
	}
}

func TestCacheBoundsBytesButKeepsTheNewest(t *testing.T) {
	var c Cache
	c.Put("a", body("x"))
	c.Put("big", etagjson.Body{Data: []byte(strings.Repeat("y", maxBytes+1)), ETag: `"big"`})
	if len(c.answers) != 1 || c.answers[0].key != "big" || c.bytes != maxBytes+1 {
		t.Fatalf("%d answers, %d bytes", len(c.answers), c.bytes)
	}
	c.Put("c", body("z"))
	if _, ok := c.Get("big"); ok {
		t.Error("the oversized answer outlived a newer one")
	}
	if c.bytes != 1 {
		t.Errorf("%d bytes", c.bytes)
	}
}

func TestCacheReplacesAKey(t *testing.T) {
	var c Cache
	c.Put("k", body("one"))
	c.Put("k", body("three"))
	if got, _ := c.Get("k"); string(got.Data) != "three" || len(c.answers) != 1 || c.bytes != 5 {
		t.Fatalf("%q, %d answers, %d bytes", got.Data, len(c.answers), c.bytes)
	}
}
