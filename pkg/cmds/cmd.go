package cmds

import (
	"context"
	_ "embed"
	"fmt"
	"github.com/go-go-golems/geppetto/pkg/steps"
	"github.com/go-go-golems/geppetto/pkg/steps/openai"
	"github.com/go-go-golems/geppetto/pkg/steps/openai/chat"
	"github.com/go-go-golems/geppetto/pkg/steps/openai/completion"
	glazedcmds "github.com/go-go-golems/glazed/pkg/cmds"
	"github.com/go-go-golems/glazed/pkg/cmds/alias"
	"github.com/go-go-golems/glazed/pkg/cmds/layers"
	"github.com/go-go-golems/glazed/pkg/cmds/loaders"
	"github.com/go-go-golems/glazed/pkg/cmds/parameters"
	"github.com/go-go-golems/glazed/pkg/helpers/templating"
	"github.com/go-go-golems/glazed/pkg/middlewares"
	"github.com/go-go-golems/glazed/pkg/settings"
	"github.com/go-go-golems/glazed/pkg/types"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
	"io"
	"strings"
)

type GeppettoCommandDescription struct {
	Name      string                            `yaml:"name"`
	Short     string                            `yaml:"short"`
	Long      string                            `yaml:"long,omitempty"`
	Flags     []*parameters.ParameterDefinition `yaml:"flags,omitempty"`
	Arguments []*parameters.ParameterDefinition `yaml:"arguments,omitempty"`
	Layers    []layers.ParameterLayer           `yaml:"layers,omitempty"`

	// TODO(manuel, 2023-02-04) This now has a hack to switch the step type
	Step *steps.StepDescription `yaml:"step,omitempty"`

	Prompt   string         `yaml:"prompt,omitempty"`
	Messages []chat.Message `yaml:"messages,omitempty"`
}

func HelpersParameterLayer() (layers.ParameterLayer, error) {
	return layers.NewParameterLayer("helpers", "pinocchio helpers",
		layers.WithFlags(
			parameters.NewParameterDefinition(
				"print-prompt",
				parameters.ParameterTypeBool,
				parameters.WithDefault(false),
				parameters.WithHelp("Print the prompt"),
			),
			parameters.NewParameterDefinition(
				"print-dyno",
				parameters.ParameterTypeBool,
				parameters.WithDefault(false),
				parameters.WithHelp("Print the dyno embed div"),
			),
		),
	)
}

type GeppettoCommand struct {
	description *glazedcmds.CommandDescription
	Factories   map[string]interface{} `yaml:"__factories,omitempty"`
	Prompt      string
}

func NewGeppettoCommand(
	description *glazedcmds.CommandDescription,
	factories map[string]interface{},
	prompt string,
) (*GeppettoCommand, error) {
	helpersParameterLayer, err := HelpersParameterLayer()
	if err != nil {
		return nil, err
	}

	glazedParameterLayer, err := settings.NewGlazedParameterLayers()
	if err != nil {
		return nil, err
	}

	description.Layers = append(description.Layers,
		helpersParameterLayer,
		glazedParameterLayer)

	return &GeppettoCommand{
		description: description,
		Factories:   factories,
		Prompt:      prompt,
	}, nil
}

//go:embed templates/dyno.tmpl.html
var dynoTemplate string

func (g *GeppettoCommand) Run(
	ctx context.Context,
	parsedLayers map[string]*layers.ParsedParameterLayer,
	ps map[string]interface{},
	gp middlewares.Processor,
) error {
	for _, f := range g.Factories {
		factory, ok := f.(steps.GenericStepFactory)
		if !ok {
			continue
		}
		err := factory.UpdateFromParameters(ps)
		if err != nil {
			return err
		}
	}

	// TODO(manuel, 2023-03-28) This is entirely completion for now...
	openaiCompletionStepFactory_, ok := g.Factories["openai-completion-step"]
	if !ok {
		return errors.Errorf("No openai-completion-step factory defined")
	}
	openaiCompletionStepFactory, ok := openaiCompletionStepFactory_.(steps.StepFactory[string, string])
	if !ok {
		return errors.Errorf("openai-completion-step factory is not a StepFactory[string, string]")
	}

	// TODO(manuel, 2023-01-28) here we would overload the factory settings with stuff passed on the CLI
	// (say, temperature or model). This would probably be part of the API for the factory, in general the
	// factory is the central abstraction of the entire system
	s, err := openaiCompletionStepFactory.NewStep()
	if err != nil {
		return err
	}

	// TODO(manuel, 2023-02-04) All this could be handle by some prompt renderer kind of thing
	promptTemplate, err := templating.CreateTemplate("prompt").Parse(g.Prompt)
	if err != nil {
		return err
	}

	// TODO(manuel, 2023-02-04) This is where multisteps would work differently, since
	// the prompt would be rendered at execution time
	var promptBuffer strings.Builder
	err = promptTemplate.Execute(&promptBuffer, ps)
	if err != nil {
		return err
	}

	printPrompt, ok := ps["print-prompt"]
	if ok && printPrompt.(bool) {
		fmt.Println(promptBuffer.String())
		return nil
	}

	printDyno, ok := ps["print-dyno"]
	if ok && printDyno.(bool) {
		openaiCompletionStepFactory__, ok := openaiCompletionStepFactory_.(*completion.StepFactory)
		if !ok {
			return errors.Errorf("openai-completion-step factory is not a StepFactory")
		}
		settings := openaiCompletionStepFactory__.StepSettings

		dyno, err := templating.RenderHtmlTemplateString(dynoTemplate, map[string]interface{}{
			"initialPrompt":   promptBuffer.String(),
			"initialResponse": "",
			"maxTokens":       settings.MaxResponseTokens,
			"temperature":     settings.Temperature,
			"topP":            settings.TopP,
			"model":           settings.Engine,
		})
		if err != nil {
			return err
		}
		fmt.Println(dyno)
		return nil
	}

	eg, ctx2 := errgroup.WithContext(ctx)
	prompt := promptBuffer.String()
	//fmt.Printf("Prompt:\n\n%s\n\n", prompt)

	eg.Go(func() error {
		return s.Run(ctx2, prompt)
	})

	accumulate := ""

	openAILayer, ok := parsedLayers["openai-completion"]
	if !ok {
		return errors.Errorf("No openai layer")
	}
	isStream := openAILayer.Parameters["openai-stream"].(bool)
	log.Debug().Bool("isStream", isStream).Msg("")

	eg.Go(func() error {
		for {
			select {
			case <-ctx2.Done():
				return ctx2.Err()
			case result := <-s.GetOutput():
				if !result.Ok() {
					return result.Error()
				}

				v, err := result.Value()
				if err != nil {
					return err
				}

				if result.IsPartial() {
					fmt.Print(v)
					accumulate += v
				} else {
					if !isStream {
						err = gp.AddRow(ctx, types.NewRow(
							types.MRP("response", accumulate+v),
						))
					}

					if err != nil {
						return err
					}
					return nil

				}
			}

		}
	})
	return eg.Wait()
}

func (g *GeppettoCommand) Description() *glazedcmds.CommandDescription {
	return g.description
}

type GeppettoCommandLoader struct {
}

func (g *GeppettoCommandLoader) LoadCommandFromYAML(
	s io.Reader,
	options ...glazedcmds.CommandDescriptionOption,
) ([]glazedcmds.Command, error) {
	yamlContent, err := io.ReadAll(s)
	if err != nil {
		return nil, err
	}

	buf := strings.NewReader(string(yamlContent))
	scd := &GeppettoCommandDescription{
		Flags:     []*parameters.ParameterDefinition{},
		Arguments: []*parameters.ParameterDefinition{},
	}
	err = yaml.NewDecoder(buf).Decode(scd)
	if err != nil {
		return nil, err
	}

	// TODO(manuel, 2023-01-27): There has to be a better way to parse YAML factories
	// maybe the easiest is just going to be to make them a separate file in the bundle format, really
	// rewind to read the factories...
	buf = strings.NewReader(string(yamlContent))
	completionStepFactory, err := completion.NewStepFactoryFromYAML(buf)
	if err != nil {
		return nil, err
	}

	// check if the openai-api-key is set in viper
	openaiAPIKey := viper.GetString("openai-api-key")
	if openaiAPIKey != "" {
		completionStepFactory.ClientSettings.APIKey = &openaiAPIKey
	}

	completionParameterLayer, err := completion.NewParameterLayer(
		layers.WithDefaults(completionStepFactory.StepSettings),
	)
	if err != nil {
		return nil, err
	}

	clientParameterLayer, err := openai.NewClientParameterLayer(
		layers.WithDefaults(completionStepFactory.ClientSettings),
	)
	if err != nil {
		return nil, err
	}

	ls := append(scd.Layers, completionParameterLayer, clientParameterLayer)

	factories := map[string]interface{}{}
	if completionStepFactory != nil {
		factories["openai-completion-step"] = completionStepFactory
	}

	options_ := []glazedcmds.CommandDescriptionOption{
		glazedcmds.WithShort(scd.Short),
		glazedcmds.WithLong(scd.Long),
		glazedcmds.WithFlags(scd.Flags...),
		glazedcmds.WithArguments(scd.Arguments...),
		glazedcmds.WithLayers(ls...),
	}

	description := glazedcmds.NewCommandDescription(
		scd.Name,
		options_...,
	)

	sq, err := NewGeppettoCommand(description, factories, scd.Prompt)
	if err != nil {
		return nil, err
	}

	for _, option := range options {
		option(sq.Description())
	}

	return []glazedcmds.Command{sq}, nil
}

func (g *GeppettoCommandLoader) LoadCommandAliasFromYAML(s io.Reader, options ...alias.Option) ([]*alias.CommandAlias, error) {
	return loaders.LoadCommandAliasFromYAML(s, options...)
}
