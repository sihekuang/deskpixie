package compat

import "testing"

func TestTheLabelNamesTheOldestSupportedVersion(t *testing.T) {
	if SupportedVersions != "tmux 3.2 or newer" {
		t.Fatal(SupportedVersions)
	}
}

func TestVersionsAreReadFromTmuxDashV(t *testing.T) {
	for out, want := range map[string]Version{
		"tmux 3.2\n":       {3, 2, true},
		"tmux 3.3a\n":      {3, 3, true},
		"tmux 3.5a":        {3, 5, true},
		"tmux 3.1c\n":      {3, 1, true},
		"tmux 2.9":         {2, 9, true},
		"tmux 10.0":        {10, 0, true},
		"tmux next-3.6\n":  {3, 6, true},
		"tmux openbsd-7.4": {0, 0, false}, // OpenBSD's base tmux: no upstream number
		"tmux master\n":    {0, 0, false},
		"":                 {0, 0, false},
		"zsh 5.9":          {0, 0, false},
	} {
		if got := Parse(out); got != want {
			t.Errorf("%q: %+v, want %+v", out, got, want)
		}
	}
}

func TestOnlyTmux32AndNewerIsSupported(t *testing.T) {
	for v, ok := range map[Version]bool{
		{3, 2, true}: true, {3, 5, true}: true, {4, 0, true}: true,
		{3, 1, true}: false, {2, 9, true}: false,
		// A build that doesn't say (master, a distro's own label) is given the benefit of the doubt.
		{0, 0, false}: true,
	} {
		if Supported(v) != ok {
			t.Errorf("%+v: %v", v, !ok)
		}
	}
}
