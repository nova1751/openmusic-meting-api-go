package migrate

import (
	"cmp"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Contribution struct {
	Key, ProviderName, RevokeToken string
}

type ContributionSummary struct {
	ProviderName string `json:"providerName"`
	Platform     string `json:"platform"`
	Tier         string `json:"tier"`
	UpdatedAt    int64  `json:"updatedAt"`
	Source       string `json:"source"`
}

func Contributions(dataDir string, limit int) ([]ContributionSummary, error) {
	var items map[string]struct {
		nodeAccount
		ProviderName string
		CreatedBy    string
	}
	err := readJSON(filepath.Join(dataDir, "cookies.json"), &items)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]ContributionSummary, 0)
	for _, item := range items {
		if item.IsValid != nil && !*item.IsValid ||
			item.Source != "contribution" && item.ContributionKey == "" && !strings.HasPrefix(item.Note, "contribution:") {
			continue
		}
		name := item.ProviderName
		if name == "" {
			name = item.CreatedBy
		}
		if name == "" {
			name = "匿名用户"
		}
		tier := "vip"
		if accountRank(item.nodeAccount) == 300 {
			tier = "svip"
		}
		updated := item.UpdatedAt
		if updated == 0 {
			updated = item.CreatedAt
		}
		out = append(out, ContributionSummary{ProviderName: name, Platform: item.Platform, Tier: tier, UpdatedAt: updated, Source: "homepage"})
	}
	slices.SortFunc(out, func(a, b ContributionSummary) int { return cmp.Compare(b.UpdatedAt, a.UpdatedAt) })
	return out[:min(len(out), limit)], nil
}

func RevokeContribution(dataDir, revokeToken string) error {
	path := filepath.Join(dataDir, "cookies.json")
	items, err := entries(path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(revokeToken))
	id := ""
	for key, item := range items {
		var source, token string
		_ = json.Unmarshal(item["source"], &source)
		_ = json.Unmarshal(item["contributionToken"], &token)
		candidate := sha256.Sum256([]byte(token))
		if source == "contribution" && subtle.ConstantTimeCompare(digest[:], candidate[:]) == 1 {
			if id != "" {
				return errors.New("duplicate contribution revocation token")
			}
			id = key
		}
	}
	if id == "" {
		return os.ErrNotExist
	}
	delete(items, id)
	return atomicJSON(path, items)
}
