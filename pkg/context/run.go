package context

import (
	"bytes"
	context2 "context"
	"github.com/go-go-golems/bobatea/pkg/chat/conversation"
	"github.com/go-go-golems/geppetto/pkg/steps"
	"github.com/go-go-golems/glazed/pkg/helpers/maps"
	"github.com/go-go-golems/glazed/pkg/helpers/templating"
	"io"
	"strings"
)

type GeppettoRunnable interface {
	RunWithManager(ctx context2.Context, manager *conversation.Manager) (steps.StepResult[string], error)
}

// CreateManager creates a new Context Manager. It is used by the code generator
// to initialize a conversation by passing a custom glazed struct for params.
//
// The systemPrompt and prompt templates are rendered using the params.
// Messages are also rendered using the params before being added to the manager.
//
// ManagerOptions can be passed to further customize the manager on creation.
func CreateManager(
	systemPrompt string,
	prompt string,
	messages []*conversation.Message,
	params interface{},
	options ...conversation.ManagerOption,
) (*conversation.Manager, error) {
	// convert the params to map[string]interface{}
	var ps map[string]interface{}
	if _, ok := params.(map[string]interface{}); !ok {
		var err error
		ps, err = maps.GlazedStructToMap(params)
		if err != nil {
			return nil, err
		}
	} else {
		ps = params.(map[string]interface{})
	}

	manager := conversation.NewManager()

	if systemPrompt != "" {
		systemPromptTemplate, err := templating.CreateTemplate("system-prompt").Parse(systemPrompt)
		if err != nil {
			return nil, err
		}

		var systemPromptBuffer strings.Builder
		err = systemPromptTemplate.Execute(&systemPromptBuffer, ps)
		if err != nil {
			return nil, err
		}

		// TODO(manuel, 2023-12-07) Only do this conditionally, or maybe if the system prompt hasn't been set yet, if you use an agent.
		manager.AddMessages(conversation.NewMessage(systemPromptBuffer.String(), conversation.RoleSystem))
	}

	for _, message := range messages {
		messageTemplate, err := templating.CreateTemplate("message").Parse(message.Text)
		if err != nil {
			return nil, err
		}

		var messageBuffer strings.Builder
		err = messageTemplate.Execute(&messageBuffer, ps)
		if err != nil {
			return nil, err
		}
		s_ := messageBuffer.String()

		manager.AddMessages(conversation.NewMessage(s_, message.Role, conversation.WithTime(message.Time)))
	}

	// render the prompt
	if prompt != "" {
		// TODO(manuel, 2023-02-04) All this could be handle by some prompt renderer kind of thing
		promptTemplate, err := templating.CreateTemplate("prompt").Parse(prompt)
		if err != nil {
			return nil, err
		}

		// TODO(manuel, 2023-02-04) This is where multisteps would work differently, since
		// the prompt would be rendered at execution time
		var promptBuffer strings.Builder
		err = promptTemplate.Execute(&promptBuffer, ps)
		if err != nil {
			return nil, err
		}

		manager.AddMessages(conversation.NewMessage(promptBuffer.String(), conversation.RoleUser))
	}

	for _, option := range options {
		option(manager)
	}

	return manager, nil
}

func RunIntoWriter(
	ctx context2.Context,
	c GeppettoRunnable,
	manager *conversation.Manager,
	w io.Writer,
) error {
	stepResult, err := c.RunWithManager(ctx, manager)
	if err != nil {
		return err
	}

	for {
		select {
		case r, ok := <-stepResult.GetChannel():
			if !ok {
				return nil
			}
			if r.Error() != nil {
				return r.Error()
			}

			s, err := r.Value()
			if err != nil {
				return err
			}
			_, err = w.Write([]byte(s))
			if err != nil {
				return err
			}

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func RunToString(
	ctx context2.Context,
	c GeppettoRunnable,
	manager *conversation.Manager,
) (string, error) {
	var b []byte
	w := bytes.NewBuffer(b)
	err := RunIntoWriter(ctx, c, manager, w)
	if err != nil {
		return "", err
	}

	return w.String(), nil
}

func RunToContextManager(
	ctx context2.Context,
	c GeppettoRunnable,
	manager *conversation.Manager,
) (*conversation.Manager, error) {
	s, err := RunToString(ctx, c, manager)
	if err != nil {
		return nil, err
	}

	manager.AddMessages(conversation.NewMessage(s, conversation.RoleAssistant))

	return manager, nil
}
