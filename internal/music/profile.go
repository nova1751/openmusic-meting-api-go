package music

import "context"

// AccountProfile is the validated subset that can be saved in the existing
// Node account store.
type AccountProfile struct {
	UserID      string `json:"userId"`
	Nickname    string `json:"nickname"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	IsVIP       bool   `json:"isVip"`
	IsSVIP      bool   `json:"isSvip"`
	CanPlayVIP  bool   `json:"canPlayVip"`
	CanPlaySVIP bool   `json:"canPlaySvip"`
}

type AccountValidator func(context.Context, string) (AccountProfile, error)
