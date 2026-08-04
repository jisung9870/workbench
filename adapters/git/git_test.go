package git

import "testing"

func TestParsePorcelainWithSpacesAndFlags(t *testing.T) {
	input := "worktree /tmp/project with spaces\x00HEAD abc123\x00branch refs/heads/feature/test\x00locked maintenance\x00\x00" +
		"worktree /tmp/detached\x00HEAD def456\x00detached\x00prunable missing gitdir\x00\x00"
	items, err := parsePorcelain(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("unexpected items: %#v", items)
	}
	if items[0].Path != "/tmp/project with spaces" || items[0].Branch != "feature/test" || !items[0].Locked || items[0].LockReason != "maintenance" {
		t.Fatalf("unexpected linked worktree: %#v", items[0])
	}
	if !items[1].Detached || !items[1].Prunable || items[1].PruneReason != "missing gitdir" {
		t.Fatalf("unexpected detached worktree: %#v", items[1])
	}
}

func TestParsePorcelainRejectsMissingPath(t *testing.T) {
	if _, err := parsePorcelain("HEAD abc123\x00branch refs/heads/main\x00\x00"); err == nil {
		t.Fatal("expected missing worktree path to fail")
	}
}
