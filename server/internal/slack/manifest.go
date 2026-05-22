package slack

// Manifest is the subset of Slack's app manifest schema we generate per
// agent. We intentionally keep it minimal: only the scopes and event
// subscriptions the cost-guarded ingress needs. Adding new capabilities
// later means a manifest update — but no migration to schema.
//
// Slack's manifest API accepts either JSON or YAML; we send JSON so the
// canonical Go encoder handles escaping.
type Manifest struct {
	DisplayInformation ManifestDisplayInformation `json:"display_information"`
	Features           ManifestFeatures           `json:"features"`
	OAuthConfig        ManifestOAuthConfig        `json:"oauth_config"`
	Settings           ManifestSettings           `json:"settings"`
}

type ManifestDisplayInformation struct {
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	BackgroundColor string `json:"background_color,omitempty"`
}

type ManifestFeatures struct {
	BotUser ManifestBotUser `json:"bot_user"`
}

type ManifestBotUser struct {
	DisplayName  string `json:"display_name"`
	AlwaysOnline bool   `json:"always_online"`
}

type ManifestOAuthConfig struct {
	RedirectURLs []string                `json:"redirect_urls"`
	Scopes       ManifestOAuthConfigScopes `json:"scopes"`
}

type ManifestOAuthConfigScopes struct {
	Bot []string `json:"bot"`
}

type ManifestSettings struct {
	EventSubscriptions ManifestEventSubscriptions `json:"event_subscriptions"`
	Interactivity      ManifestInteractivity      `json:"interactivity"`
	OrgDeployEnabled   bool                       `json:"org_deploy_enabled"`
	SocketModeEnabled  bool                       `json:"socket_mode_enabled"`
	TokenRotationEnabled bool                     `json:"token_rotation_enabled"`
}

type ManifestEventSubscriptions struct {
	RequestURL string   `json:"request_url"`
	BotEvents  []string `json:"bot_events"`
}

type ManifestInteractivity struct {
	IsEnabled bool `json:"is_enabled"`
}

// AgentManifestInput is the agent-side data the handler hands us to build
// a manifest. Keeping this a plain struct (no agent DB row) keeps the
// slack package independent of generated DB types.
type AgentManifestInput struct {
	DisplayName string // e.g. "Ada" — surfaces as the Slack bot name
	Description string
	WebhookURL  string // /api/webhooks/slack/{agent_id}
	RedirectURL string // /api/slack/oauth/callback
}

// BuildAgentManifest constructs a manifest with the cost-guarded scope:
// only the three event types our dispatcher accepts, plus the matching
// bot scopes for chat.postMessage and users.info.
func BuildAgentManifest(in AgentManifestInput) Manifest {
	return Manifest{
		DisplayInformation: ManifestDisplayInformation{
			Name:        in.DisplayName,
			Description: in.Description,
		},
		Features: ManifestFeatures{
			BotUser: ManifestBotUser{
				DisplayName:  in.DisplayName,
				AlwaysOnline: true,
			},
		},
		OAuthConfig: ManifestOAuthConfig{
			RedirectURLs: []string{in.RedirectURL},
			Scopes: ManifestOAuthConfigScopes{
				Bot: []string{
					"app_mentions:read",
					"chat:write",
					"chat:write.customize",
					"im:history",
					"im:read",
					"channels:history",
					"channels:read",
					"groups:history",
					"groups:read",
					"mpim:read",
					"users:read",
					"users:read.email",
				},
			},
		},
		Settings: ManifestSettings{
			EventSubscriptions: ManifestEventSubscriptions{
				RequestURL: in.WebhookURL,
				BotEvents: []string{
					"app_mention",
					"message.im",
					"message.channels",
				},
			},
			Interactivity:        ManifestInteractivity{IsEnabled: false},
			OrgDeployEnabled:     false,
			SocketModeEnabled:    false,
			TokenRotationEnabled: false,
		},
	}
}
