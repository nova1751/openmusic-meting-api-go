package music

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

type Membership string

const (
	Free Membership = "free"
	VIP  Membership = "vip"
	SVIP Membership = "svip"
)

type Account struct {
	Cookie     string     `json:"cookie"`
	Membership Membership `json:"membership"`
	FMPriority bool       `json:"fmPriority"`
}

type AccountPool struct {
	accounts   []Account
	next       atomic.Uint64
	mu         sync.Mutex
	cooldown   map[int]time.Time
	loader     func() ([]Account, error)
	generation uint64
}

func NewAccountPool(accounts []Account) *AccountPool {
	return &AccountPool{accounts: slices.Clone(accounts), cooldown: make(map[int]time.Time)}
}

// SetLoader enables read-only refresh of externally managed accounts.
func (p *AccountPool) SetLoader(loader func() ([]Account, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loader = loader
}

func (p *AccountPool) Lookup(ctx context.Context, req Request, provider Provider) (Result, error) {
	if req.Cookie != "" {
		return provider.Lookup(ctx, req)
	}
	p.mu.Lock()
	if p.loader != nil {
		accounts, err := p.loader()
		if err != nil {
			p.mu.Unlock()
			return Result{}, err
		}
		if !slices.Equal(accounts, p.accounts) {
			p.accounts = slices.Clone(accounts)
			p.cooldown = make(map[int]time.Time)
			p.generation++
		}
	}
	accounts := slices.Clone(p.accounts)
	generation := p.generation
	p.mu.Unlock()
	switch req.Type {
	case "url":
		return p.play(ctx, req, provider, accounts)
	case "fm":
		return p.fm(ctx, req, provider, accounts, generation)
	}
	if len(accounts) > 0 {
		req.Cookie = accounts[0].Cookie
	}
	return provider.Lookup(ctx, req)
}

func (p *AccountPool) play(ctx context.Context, req Request, provider Provider, accounts []Account) (Result, error) {
	var eligible []int
	for index, account := range accounts {
		if account.Membership == SVIP ||
			(account.Membership == VIP && RequiredMembershipForPlatform(req.Platform, req.Quality) != SVIP) {
			eligible = append(eligible, index)
		}
	}
	if len(eligible) == 0 && req.Platform == "tencent" {
		for index, account := range accounts {
			if account.Membership == Free {
				eligible = append(eligible, index)
			}
		}
	}
	if len(eligible) == 0 {
		if req.Platform == "tencent" {
			return provider.Lookup(ctx, req)
		}
		return Result{}, ErrNoAccount
	}
	start := int((p.next.Add(1) - 1) % uint64(len(eligible)))
	ordered := make([]int, 0, len(eligible))
	ordered = append(ordered, eligible[start:]...)
	ordered = append(ordered, eligible[:start]...)
	if req.FM {
		if pos := slices.IndexFunc(ordered, func(index int) bool { return accounts[index].FMPriority }); pos >= 0 {
			ordered[0], ordered[pos] = ordered[pos], ordered[0]
		}
	}
	req.StrictQuality = req.Platform == "tencent" && RequiredMembershipForPlatform(req.Platform, req.Quality) == SVIP
	var failed error
	var miss bool
	var downgrade []int
	try := func(index int) (Result, error) {
		req.Cookie = accounts[index].Cookie
		result, err := provider.Lookup(ctx, req)
		if err == nil && result.Link.URL != "" {
			result.PlaybackCookie = req.Cookie
			return result, nil
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if errors.Is(err, ErrInvalidID) {
			return Result{}, err
		}
		switch {
		case err == nil || errors.Is(err, ErrNoURL):
			miss = true
		case !errors.Is(err, ErrAccountInvalid):
			failed = err
		}
		if req.StrictQuality && !errors.Is(err, ErrVerificationRequired) && !errors.Is(err, ErrAccountInvalid) {
			downgrade = append(downgrade, index)
		}
		return Result{}, nil
	}
	for _, index := range ordered[:min(4, len(ordered))] {
		if result, err := try(index); err != nil || result.Link.URL != "" {
			return result, err
		}
	}
	req.StrictQuality = false
	for _, index := range downgrade {
		if result, err := try(index); err != nil || result.Link.URL != "" {
			return result, err
		}
	}
	if miss {
		return Result{}, ErrNoURL
	}
	if failed == nil {
		return Result{}, ErrNoAccount
	}
	return Result{}, failed
}

func (p *AccountPool) fm(ctx context.Context, req Request, provider Provider, accounts []Account, generation uint64) (Result, error) {
	now := time.Now()
	p.mu.Lock()
	var ordered []int
	for index, account := range accounts {
		if p.cooldown[index].After(now) {
			continue
		}
		if account.FMPriority {
			ordered = append([]int{index}, ordered...)
		} else {
			ordered = append(ordered, index)
		}
	}
	p.mu.Unlock()
	if len(ordered) == 0 {
		return Result{}, ErrNoAccount
	}
	var failed error
	var empty bool
	for attempt, index := range ordered {
		account := accounts[index]
		if attempt > 0 && account.Membership != VIP && account.Membership != SVIP {
			continue
		}
		req.Cookie = account.Cookie
		result, err := provider.Lookup(ctx, req)
		if err == nil && len(result.Items) > 0 {
			p.mu.Lock()
			if generation == p.generation {
				delete(p.cooldown, index)
			}
			p.mu.Unlock()
			return result, nil
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		p.mu.Lock()
		if generation == p.generation {
			p.cooldown[index] = time.Now().Add(30 * time.Second)
		}
		p.mu.Unlock()
		if err == nil {
			empty = true
		} else if !errors.Is(err, ErrAccountInvalid) {
			failed = err
		}
	}
	if empty {
		return Result{Items: []Item{}}, nil
	}
	if failed == nil {
		return Result{}, ErrNoAccount
	}
	return Result{}, failed
}
