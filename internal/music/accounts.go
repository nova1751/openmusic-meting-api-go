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
	accounts []Account
	next     atomic.Uint64
	mu       sync.Mutex
	cooldown map[int]time.Time
}

func NewAccountPool(accounts []Account) *AccountPool {
	return &AccountPool{accounts: slices.Clone(accounts), cooldown: make(map[int]time.Time)}
}

func (p *AccountPool) Lookup(ctx context.Context, req Request, provider Provider) (Result, error) {
	if req.Cookie != "" {
		return provider.Lookup(ctx, req)
	}
	switch req.Type {
	case "url":
		return p.play(ctx, req, provider)
	case "fm":
		return p.fm(ctx, req, provider)
	}
	if len(p.accounts) > 0 {
		req.Cookie = p.accounts[0].Cookie
	}
	return provider.Lookup(ctx, req)
}

func (p *AccountPool) play(ctx context.Context, req Request, provider Provider) (Result, error) {
	var eligible []int
	for index, account := range p.accounts {
		if account.Membership == SVIP ||
			(account.Membership == VIP && RequiredMembership(req.Quality) != SVIP) {
			eligible = append(eligible, index)
		}
	}
	if len(eligible) == 0 {
		return Result{}, ErrNoAccount
	}
	start := int((p.next.Add(1) - 1) % uint64(len(eligible)))
	ordered := make([]int, 0, len(eligible))
	ordered = append(ordered, eligible[start:]...)
	ordered = append(ordered, eligible[:start]...)
	if req.FM {
		if pos := slices.IndexFunc(ordered, func(index int) bool { return p.accounts[index].FMPriority }); pos >= 0 {
			ordered[0], ordered[pos] = ordered[pos], ordered[0]
		}
	}
	var failed error
	var miss bool
	for _, index := range ordered[:min(4, len(ordered))] {
		req.Cookie = p.accounts[index].Cookie
		result, err := provider.Lookup(ctx, req)
		switch {
		case err == nil && result.Link.URL != "":
			return result, nil
		case err == nil || errors.Is(err, ErrNoURL):
			miss = true
		case errors.Is(err, ErrInvalidID):
			return Result{}, err
		case errors.Is(err, ErrAccountInvalid):
		default:
			failed = err
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
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

func (p *AccountPool) fm(ctx context.Context, req Request, provider Provider) (Result, error) {
	now := time.Now()
	p.mu.Lock()
	var ordered []int
	for index, account := range p.accounts {
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
		account := p.accounts[index]
		if attempt > 0 && account.Membership != VIP && account.Membership != SVIP {
			continue
		}
		req.Cookie = account.Cookie
		result, err := provider.Lookup(ctx, req)
		if err == nil && len(result.Items) > 0 {
			p.mu.Lock()
			delete(p.cooldown, index)
			p.mu.Unlock()
			return result, nil
		}
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		p.mu.Lock()
		p.cooldown[index] = time.Now().Add(30 * time.Second)
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
