package buildinfo

import (
	"runtime/debug"
	"testing"
)

func TestAnUnstampedBuildIsDev(t *testing.T) {
	if Version() != "dev" {
		t.Fatal(Version())
	}
}

func TestTheStampWins(t *testing.T) {
	version, commit = "0.19.0", "abc1234"
	t.Cleanup(func() { version, commit = "", "" })
	if got := String(); got != "0.19.0 (abc1234)" {
		t.Fatal(got)
	}
}

func TestGoRecordsTheCommitWhenNoneWasStamped(t *testing.T) {
	for _, c := range []struct {
		settings []debug.BuildSetting
		want     string
	}{
		{[]debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}}, "0123456"},
		{[]debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}, {Key: "vcs.modified", Value: "true"}}, "0123456-dirty"},
		{[]debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}, ""},
		{nil, ""},
	} {
		if got := vcsCommit(c.settings); got != c.want {
			t.Errorf("%v: %q, want %q", c.settings, got, c.want)
		}
	}
}

func TestFormat(t *testing.T) {
	if Format("dev", "") != "dev" || Format("1.2.3", "abc") != "1.2.3 (abc)" {
		t.Fatal(Format("dev", ""), Format("1.2.3", "abc"))
	}
}
