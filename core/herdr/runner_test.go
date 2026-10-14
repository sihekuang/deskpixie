package herdr

import (
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Port of runner.rs tests (6).

func TestAPatientRunnerUsesItsOwnTimeout(t *testing.T) {
	sleep := []string{"/bin/sleep", "0.3"}
	out, err := PatientRunner(5*time.Second).Run(sleep, nil)
	if err != nil || out.Status != 0 {
		t.Fatalf("%v %v", out, err)
	}
	_, err = PatientRunner(50*time.Millisecond).Run(sleep, nil)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("%v", err)
	}
}

func TestRunByGivesUpAtTheDeadlineOrAtItsOwnTimeout(t *testing.T) {
	sleep := []string{"/bin/sleep", "5"}
	started := time.Now()
	_, err := PatientRunner(60*time.Second).RunBy(sleep, nil, time.Now().Add(200*time.Millisecond))
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(started) >= 2*time.Second {
		t.Fatalf("%v after %v", err, time.Since(started))
	}
	_, err = PatientRunner(100*time.Millisecond).RunBy(sleep, nil, time.Now().Add(60*time.Second))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("%v", err)
	}
	_, err = ProcessRunner{}.RunBy([]string{"/usr/bin/true"}, nil, time.Now())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("nothing runs past the deadline: %v", err)
	}
}

func TestRunUsesExactlyTheGivenEnvironment(t *testing.T) {
	env := []agent.EnvVar{{Key: "ONLY_THIS", Value: "1"}}
	out, err := ProcessRunner{}.Run([]string{"/usr/bin/env"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != 0 {
		t.Fatalf("status %d", out.Status)
	}
	if strings.TrimSpace(out.Stdout) != "ONLY_THIS=1" {
		t.Fatalf("%q", out.Stdout)
	}
	// No env at all is an empty environment, never the parent's.
	out, err = ProcessRunner{}.Run([]string{"/usr/bin/env"}, nil)
	if err != nil || strings.TrimSpace(out.Stdout) != "" {
		t.Fatalf("%q %v", out.Stdout, err)
	}
}

func TestASlowProgramIsKilledAtTheDeadline(t *testing.T) {
	started := time.Now()
	_, err := ProcessRunner{}.RunWithTimeout([]string{"/bin/sleep", "5"}, nil, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("%v", err)
	}
	if time.Since(started) >= 2*time.Second {
		t.Fatal(time.Since(started))
	}
}

func TestTheWholeProcessGroupDiesAtTheDeadline(t *testing.T) {
	started := time.Now()
	sh := []string{"/bin/sh", "-c", "sleep 5 & wait"}
	if _, err := (ProcessRunner{}).RunWithTimeout(sh, []agent.EnvVar{{Key: "PATH", Value: "/bin:/usr/bin"}}, 200*time.Millisecond); err == nil {
		t.Fatal("want an error")
	}
	if time.Since(started) >= 2*time.Second {
		t.Fatal(time.Since(started))
	}
}

func TestAMissingProgramIsAnError(t *testing.T) {
	_, err := ProcessRunner{}.Run([]string{"/definitely/not/here"}, nil)
	// The supervisor maps exactly this (ENOENT) to HostError NotFound.
	if err == nil || !strings.Contains(err.Error(), "(os error 2)") {
		t.Fatalf("%v", err)
	}
	if _, err := (ProcessRunner{}).Run(nil, nil); err == nil {
		t.Fatal("an empty command is an error")
	}
}
