package claude

import (
	_ "embed"
	"github.com/go-go-golems/glazed/pkg/cmds/layers"
)

type Settings struct {
	TopK    *int    `yaml:"top_k,omitempty" glazed.parameter:"claude-top-k"`
	UserID  *string `yaml:"user_id,omitempty" glazed.parameter:"claude-user-id"`
	BaseURL *string `yaml:"base_url,omitempty" glazed.parameter:"claude-base-url"`
	APIKey  *string `yaml:"api_key,omitempty" glazed.parameter:"claude-api-key"`
}

func NewSettings() *Settings {
	return &Settings{
		TopK:   nil,
		UserID: nil,
	}
}

func (s *Settings) Clone() *Settings {
	return &Settings{
		TopK:    s.TopK,
		UserID:  s.UserID,
		BaseURL: s.BaseURL,
		APIKey:  s.APIKey,
	}
}

const ClaudeChatSlug = "claude-chat"

//go:embed "claude.yaml"
var settingsYAML []byte

type ParameterLayer struct {
	*layers.ParameterLayerImpl `yaml:",inline"`
}

func NewParameterLayer(options ...layers.ParameterLayerOptions) (*ParameterLayer, error) {
	ret, err := layers.NewParameterLayerFromYAML(settingsYAML, options...)
	if err != nil {
		return nil, err
	}

	return &ParameterLayer{
		ParameterLayerImpl: ret,
	}, nil
}
