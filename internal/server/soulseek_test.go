package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/arr"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

func TestSoulseekFolderMatch(t *testing.T) {
	cases := []struct {
		dir, artist, title string
		want               bool
	}{
		{`@@abcde\Music\Radiohead\2007 - In Rainbows [FLAC]`, "Radiohead", "In Rainbows", true},
		{`Music\FLAC\Radiohead - In Rainbows (2007) 24-96`, "Radiohead", "In Rainbows", true},
		{`Music\Radiohead\Live in Rainbows`, "Radiohead", "In Rainbows", true}, // a superset; the newest match wins anyway
		{`Music\Vitamin String Quartet\Performs In Rainbows`, "Radiohead", "In Rainbows", false},
		{`Music\Radiohead\OK Computer`, "Radiohead", "In Rainbows", false},
		{`Downloads\AC-DC\Highway to Hell`, "AC/DC", "Highway to Hell", true},
		{`share\Björk\Homogenic`, "Björk", "Homogenic", true},
	}
	for _, c := range cases {
		d := arr.SoulseekDownload{Directory: c.dir}
		if got := d.MatchesAlbum(c.artist, c.title); got != c.want {
			t.Errorf("%q vs %s – %s = %v, want %v", c.dir, c.artist, c.title, got, c.want)
		}
	}
}

func TestSoulseekView(t *testing.T) {
	now := time.Now().UTC()
	active := &arr.SoulseekDownload{Username: "vinyl_fan", Files: 10, Done: 4, Active: 1, Queued: 5, Size: 1000, Received: 400, Speed: 100}
	v := soulseekView(view{}, active)
	if v == nil || v.state != "downloading" || v.progress != 0.4 || !strings.Contains(v.detail, "Soulseek (from vinyl_fan)") {
		t.Fatalf("active = %+v", v)
	}
	queued := &arr.SoulseekDownload{Username: "u", Files: 10, Queued: 10, Size: 1000}
	if v := soulseekView(view{}, queued); v == nil || !strings.Contains(v.detail, "Waiting in a Soulseek queue") {
		t.Fatalf("queued = %+v", v)
	}
	done := &arr.SoulseekDownload{Files: 10, Done: 10, Size: 1000, Received: 1000, Ended: now.Add(-10 * time.Minute).Format("2006-01-02T15:04:05.9999999")}
	if v := soulseekView(view{}, done); v == nil || v.state != "importing" {
		t.Fatalf("done = %+v", v)
	}
	old := *done
	old.Ended = now.Add(-30 * time.Hour).Format(time.RFC3339)
	if v := soulseekView(view{}, &old); v != nil {
		t.Fatalf("an old finished folder still counts: %+v", v)
	}
	broken := &arr.SoulseekDownload{Files: 10, Done: 3, Failed: 7, Ended: now.Format(time.RFC3339)}
	if v := soulseekView(view{}, broken); v == nil || v.state != "searching" || !strings.Contains(v.detail, "3 of 10") {
		t.Fatalf("broken = %+v", v)
	}
	if soulseekView(view{}, nil) != nil {
		t.Fatal("no download should say nothing")
	}
}

func TestSoularrBlacklist(t *testing.T) {
	dir := t.TempDir()
	orig := `{
  "31": {"album_id": 31, "artist": "Power Quest", "title": "Blood Alliance", "failed_at": "2026-08-02T10:14:10", "folder_path": "/downloads/failed_imports/Power Quest - Blood Alliance (2011)"},
  "40": {"album_id": 40, "artist": "AC/DC", "title": "Highway to Hell", "failed_at": "2026-09-01T08:00:00", "folder_path": "/downloads/failed_imports/AC-DC - Highway to Hell"}
}`
	if err := os.WriteFile(filepath.Join(dir, "failed_imports.json"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	tb := newTestBox(t)
	tb.s.cfg.SoularrDir = dir
	bl := tb.s.soularrBlacklist()
	if len(bl) != 2 || bl[40].Title != "Highway to Hell" {
		t.Fatalf("blacklist = %+v", bl)
	}
	if !strings.Contains(soularrDetail(bl[40]), "Highway to Hell") && !strings.Contains(soularrDetail(bl[40]), "AC-DC") {
		t.Fatalf("detail = %q", soularrDetail(bl[40]))
	}
	if !tb.s.soularrBlocked(store.Request{Kind: "album", ArrID: 40}) || tb.s.soularrBlocked(store.Request{Kind: "album", ArrID: 41}) {
		t.Fatal("soularrBlocked is wrong")
	}
	removed, err := tb.s.forgetSoularrFailure(40)
	if err != nil || !removed {
		t.Fatalf("forget = %v %v", removed, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "failed_imports.json"))
	var left map[string]map[string]any
	if err := json.Unmarshal(b, &left); err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left["31"]["folder_path"] != "/downloads/failed_imports/Power Quest - Blood Alliance (2011)" {
		t.Fatalf("other entries changed: %v", left)
	}
	if removed, _ := tb.s.forgetSoularrFailure(40); removed {
		t.Fatal("removed twice")
	}
	if _, err := os.Stat(filepath.Join(dir, "failed_imports.json.pocket-requests.tmp")); err == nil {
		t.Fatal("temp file left behind")
	}
}
