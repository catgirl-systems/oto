package config

// Text tools are scoped by the same exact server/account key as chat history.
type CommunityTextTools struct {
	CTCPVersion   bool                    `json:"ctcp_version"`
	Keywords      []string                `json:"keywords"`
	Substitutions []CommunitySubstitution `json:"substitutions"`
	Censorship    []string                `json:"censorship"`
}
type CommunitySubstitution struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type CommunityAway struct {
	AutoAwaySeconds int    `json:"auto_away_seconds" validate:"min=0,max=86400"`
	AutoReply       string `json:"auto_reply" validate:"max=1024"`
}
