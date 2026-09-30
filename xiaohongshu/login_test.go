package xiaohongshu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestLoginStatusWaitsForTypedAccountWithoutFullPageLoad(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	navigations, observations := 0, 0
	loggedIn, err := checkLoginStatus(ctx, func() error {
		navigations++
		return nil
	}, func() (loginState, error) {
		observations++
		if observations == 1 {
			return loginState{Status: "pending"}, nil
		}
		return loginState{Status: "authenticated", UserID: "test-subject"}, nil
	})
	if err != nil || !loggedIn || navigations != 1 || observations != 2 {
		t.Fatalf("typed account readiness: loggedIn=%v err=%v nav=%d observations=%d", loggedIn, err, navigations, observations)
	}
}

func TestLoginStatusUnknownIsErrorRatherThanLoggedOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	loggedIn, err := checkLoginStatus(ctx, func() error { return nil }, func() (loginState, error) {
		return loginState{Status: "pending"}, nil
	})
	if loggedIn || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unknown must retain deadline error: loggedIn=%v err=%v", loggedIn, err)
	}
}

func TestLoginStatusExplicitGuestIsLoggedOut(t *testing.T) {
	loggedIn, err := checkLoginStatus(context.Background(), func() error { return nil }, func() (loginState, error) {
		return loginState{Status: "unauthenticated"}, nil
	})
	if loggedIn || err != nil {
		t.Fatalf("explicit guest: loggedIn=%v err=%v", loggedIn, err)
	}
}

func TestLoginStatusNavigationFailureReturnsErrorWithoutObservation(t *testing.T) {
	failure := errors.New("test navigation failed")
	observed := false
	loggedIn, err := checkLoginStatus(context.Background(), func() error { return failure }, func() (loginState, error) {
		observed = true
		return loginState{}, nil
	})
	if loggedIn || observed || !errors.Is(err, failure) || !strings.Contains(err.Error(), "navigate login status") {
		t.Fatalf("navigation failure must remain actionable: loggedIn=%v observed=%v err=%v", loggedIn, observed, err)
	}
}

func TestLoginStatusNavigationUsesTheSameDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	observed := false
	loggedIn, err := checkLoginStatus(ctx, func() error {
		<-ctx.Done()
		return nil
	}, func() (loginState, error) {
		observed = true
		return loginState{Status: "authenticated", UserID: "test-subject"}, nil
	})
	if loggedIn || observed || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("navigation consumed deadline: loggedIn=%v observed=%v err=%v", loggedIn, observed, err)
	}
}

func TestLoginStatusCancelledReadCannotBecomeAuthenticated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	loggedIn, err := checkLoginStatus(ctx, func() error { return nil }, func() (loginState, error) {
		cancel()
		return loginState{Status: "authenticated", UserID: "test-subject"}, nil
	})
	if loggedIn || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled account read: loggedIn=%v err=%v", loggedIn, err)
	}
}

func TestLoginStatusInvalidIdentityAndReadFailuresRemainUnknown(t *testing.T) {
	for _, state := range []loginState{{Status: "authenticated"}, {Status: "authenticated", UserID: " "}, {Status: "invalid"}} {
		loggedIn, err := checkLoginStatus(context.Background(), func() error { return nil }, func() (loginState, error) { return state, nil })
		if loggedIn || err == nil {
			t.Fatalf("invalid typed state cannot prove logout or login: loggedIn=%v err=%v", loggedIn, err)
		}
	}
	failure := errors.New("test read failed")
	loggedIn, err := checkLoginStatus(context.Background(), func() error { return nil }, func() (loginState, error) { return loginState{}, failure })
	if loggedIn || !errors.Is(err, failure) {
		t.Fatalf("read failure must remain unknown: loggedIn=%v err=%v", loggedIn, err)
	}
}
