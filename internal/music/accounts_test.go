package music

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type providerFunc func(Request) (Result, error)

func (f providerFunc) Lookup(_ context.Context, req Request) (Result, error) { return f(req) }

func TestPublicPlaybackUsesEntitledAccounts(t *testing.T) {
	pool := NewAccountPool([]Account{
		{Cookie: "free", Membership: Free, FMPriority: true},
		{Cookie: "vip", Membership: VIP},
		{Cookie: "svip1", Membership: SVIP},
		{Cookie: "svip2", Membership: SVIP},
	})
	var attempts []string
	p := providerFunc(func(req Request) (Result, error) {
		attempts = append(attempts, req.Cookie)
		if req.Cookie == "svip1" {
			return Result{}, ErrNoURL
		}
		return Result{Link: Link{URL: "https://example.com"}}, nil
	})
	_, err := pool.Lookup(t.Context(), Request{Type: "url", Quality: "jymaster", FM: true}, p)
	if err != nil || !reflect.DeepEqual(attempts, []string{"svip1", "svip2"}) {
		t.Fatalf("attempts %v, error %v", attempts, err)
	}
	attempts = attempts[:0]
	_, err = pool.Lookup(t.Context(), Request{Type: "url", Quality: "jymaster", Cookie: "private"}, p)
	if err != nil || !reflect.DeepEqual(attempts, []string{"private"}) {
		t.Fatalf("explicit account attempts %v, error %v", attempts, err)
	}
}

func TestFMEmptyResultCoolsOffAndRetainsEmptySemantics(t *testing.T) {
	pool := NewAccountPool([]Account{
		{Cookie: "vip", Membership: VIP},
		{Cookie: "free", Membership: Free, FMPriority: true},
	})
	var attempts []string
	p := providerFunc(func(req Request) (Result, error) {
		attempts = append(attempts, req.Cookie)
		if req.Cookie == "vip" {
			return Result{}, errors.New("upstream fixture error")
		}
		return Result{}, nil
	})
	req := Request{Type: "fm"}
	result, err := pool.Lookup(t.Context(), req, p)
	if err != nil || result.Items == nil || len(result.Items) != 0 || !reflect.DeepEqual(attempts, []string{"free", "vip"}) {
		t.Fatalf("attempts %v, result %+v, error %v", attempts, result, err)
	}
	if _, err := pool.Lookup(t.Context(), req, p); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("cooldown missing: %v", err)
	}
}

func TestTencentPicksExactQualityAcrossAccountsBeforeDowngrading(t *testing.T) {
	for _, exact := range []bool{false, true} {
		pool := NewAccountPool([]Account{{Cookie: "challenged", Membership: SVIP}, {Cookie: "fallback", Membership: SVIP}, {Cookie: "candidate", Membership: SVIP}})
		var attempts []string
		got, err := pool.Lookup(t.Context(), Request{Platform: "tencent", Type: "url", Quality: "master"}, providerFunc(func(req Request) (Result, error) {
			attempts = append(attempts, req.Cookie)
			switch {
			case req.Cookie == "challenged":
				if !req.StrictQuality {
					t.Fatal("retried an account that needs verification")
				}
				return Result{}, ErrVerificationRequired
			case req.Cookie == "candidate" && req.StrictQuality && exact:
				return Result{Link: Link{URL: "https://example.invalid/master"}}, nil
			case req.Cookie == "fallback" && !req.StrictQuality:
				return Result{Link: Link{URL: "https://example.invalid/fallback"}}, nil
			default:
				return Result{}, ErrNoURL
			}
		}))
		want := []string{"challenged", "fallback", "candidate"}
		link := "https://example.invalid/master"
		if !exact {
			want = append(want, "fallback")
			link = "https://example.invalid/fallback"
		}
		if err != nil || got.Link.URL != link || !reflect.DeepEqual(attempts, want) {
			t.Fatalf("exact=%v link=%+v attempts=%v err=%v", exact, got.Link, attempts, err)
		}
	}
}
