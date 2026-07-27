package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lianyorker/cinlan-qq-bot/internal/domain"
	"github.com/lianyorker/cinlan-qq-bot/internal/message"
	"github.com/lianyorker/cinlan-qq-bot/internal/persona"
	"github.com/lianyorker/cinlan-qq-bot/internal/pipeline"
	"github.com/lianyorker/cinlan-qq-bot/internal/platform"
	"github.com/lianyorker/cinlan-qq-bot/internal/plugin"
)

const flowStateKey = "cinlan.bot.flow"

type flowState struct {
	event          platform.Event
	platform       string
	chatType       string
	chatID         string
	groupID        string
	userID         string
	selfID         string
	messageID      string
	text           string
	promptText     string
	eventMentioned bool
	sessionID      string
	rateLimitID    string
	history        []domain.ChatMessage
	personaName    string
	providerName   string
	errorReply     string
	response       domain.AgentResponse
	reply          string
	outbound       message.Chain
	agentErr       error
	ignored        bool
	pluginHandled  bool
}

func (s *Service) newPipeline() *pipeline.Pipeline {
	return pipeline.New(
		pipeline.StageFunc{StageName: "wake", Handler: s.stageWake},
		pipeline.StageFunc{StageName: "command", Handler: s.stageCommand},
		pipeline.StageFunc{StageName: "session", Handler: s.stageSession},
		pipeline.StageFunc{StageName: "rate_limit", Handler: s.stageRateLimit},
		pipeline.StageFunc{StageName: "plugin_before", Handler: s.stagePluginBefore},
		pipeline.StageFunc{StageName: "agent", Handler: s.stageAgent},
		pipeline.StageFunc{StageName: "decorate", Handler: s.stageDecorate},
		pipeline.StageFunc{StageName: "plugin_after", Handler: s.stagePluginAfter},
		pipeline.StageFunc{StageName: "respond", Handler: s.stageRespond},
	)
}

func (s *Service) PipelineStages() []string {
	if s.flow == nil {
		return nil
	}
	return s.flow.Names()
}

func stateFrom(event *pipeline.Context) *flowState {
	if current, ok := event.Values[flowStateKey].(*flowState); ok {
		return current
	}
	current := &flowState{event: event.Event}
	event.Values[flowStateKey] = current
	return current
}

func (s *Service) stageWake(_ context.Context, event *pipeline.Context) error {
	state := stateFrom(event)
	state.event = event.Event
	state.platform = event.Event.Platform
	state.chatType = event.Event.MessageType
	state.chatID = event.Event.ChatID
	state.userID = event.Event.UserID
	state.selfID = event.Event.SelfID
	state.messageID = event.Event.MessageID
	state.errorReply = s.cfg.ErrorReply

	if !event.Event.IsChatMessage() ||
		state.userID == "" ||
		state.userID == state.selfID {
		state.ignored = true
		event.Stop("not_target_chat_message")
		return nil
	}

	requireMention := s.cfg.RequireMention
	if s.bindings != nil {
		if current, ok := s.bindings.Match(
			state.platform,
			state.selfID,
			state.chatType,
			state.chatID,
		); ok {
			state.personaName = current.Persona
			state.providerName = current.Provider
			if current.RequireMention != nil {
				requireMention = *current.RequireMention
			}
		}
	}

	switch state.chatType {
	case platform.ChatGroup:
		state.groupID = state.chatID
		if !s.cfg.GroupAllowlist.Allows(state.chatID) {
			state.ignored = true
			event.Stop("group_not_allowed")
			return nil
		}
	case platform.ChatPrivate:
		if !s.cfg.PrivateAllowlist.Allows(state.chatID) {
			state.ignored = true
			event.Stop("private_not_allowed")
			return nil
		}
	}

	if state.messageID != "" && s.duplicate(
		state.platform+":"+state.selfID+":"+state.chatType+":"+state.chatID+":"+state.messageID,
	) {
		state.ignored = true
		event.Stop("duplicate_message")
		return nil
	}

	chain := event.Event.Chain
	if chain.Empty() && strings.TrimSpace(event.Event.RawMessage) != "" {
		if parsed, err := message.ParseCQ(event.Event.RawMessage); err == nil {
			chain = parsed
		}
	}
	state.text, state.eventMentioned = chain.PlainText(state.selfID)
	if (state.chatType == platform.ChatGroup &&
		requireMention &&
		!state.eventMentioned) ||
		strings.TrimSpace(state.text) == "" {
		state.ignored = true
		event.Stop("not_mentioned_or_empty")
		return nil
	}
	state.text = strings.TrimSpace(state.text)
	if state.chatType == platform.ChatPrivate {
		state.sessionID = state.platform + ":self:" + state.selfID + ":private:" + state.chatID
		state.promptText = state.text
	} else {
		state.sessionID = state.platform + ":self:" + state.selfID + ":group:" + state.chatID
		state.promptText = groupPromptText(state.event.SenderName, state.userID, state.text)
	}
	state.rateLimitID = state.sessionID + ":actor:" + state.userID
	event.Values["tools"] = s.tools
	return nil
}

func (s *Service) stageCommand(ctx context.Context, event *pipeline.Context) error {
	state := stateFrom(event)
	if state.ignored || state.sessionID == "" {
		return nil
	}
	if s.commands != nil {
		result, matched, err := s.commands.Execute(
			ctx,
			state.event,
			state.sessionID,
			state.text,
			event.Values,
		)
		if err != nil {
			return err
		}
		if matched {
			state.pluginHandled = true
			state.reply = strings.TrimSpace(result.Reply)
			if result.Handoff {
				s.sessions.SetHandoff(state.sessionID, true)
				s.stats.handoffCount.Add(1)
				if state.reply == "" {
					state.reply = s.cfg.HandoffReply
				}
			}
			s.stats.processed.Add(1)
			return nil
		}
	}
	if s.handleCommand(
		ctx,
		state.sessionID,
		state.selfID,
		state.chatType,
		state.chatID,
		state.userID,
		state.event.SenderName,
		state.messageID,
		state.text,
	) {
		event.Stop("command_handled")
	}
	return nil
}

func (s *Service) stageSession(_ context.Context, event *pipeline.Context) error {
	state := stateFrom(event)
	if state.ignored || state.sessionID == "" {
		return nil
	}
	snapshot := s.sessions.SnapshotState(state.sessionID)
	if snapshot.Handoff {
		state.ignored = true
		event.Stop("human_handoff")
		return nil
	}
	state.history = snapshot.History
	if snapshot.Settings.Persona != "" {
		state.personaName = snapshot.Settings.Persona
	}
	if snapshot.Settings.Provider != "" {
		state.providerName = snapshot.Settings.Provider
	}
	return nil
}

func (s *Service) stageRateLimit(_ context.Context, event *pipeline.Context) error {
	state := stateFrom(event)
	if state.ignored || state.sessionID == "" {
		return nil
	}
	if !s.allowRequest(state.rateLimitID) {
		s.stats.rateLimited.Add(1)
		event.Stop("cooldown")
	}
	return nil
}

func (s *Service) stageAgent(ctx context.Context, event *pipeline.Context) error {
	state := stateFrom(event)
	if state.ignored || state.pluginHandled || state.sessionID == "" {
		return nil
	}
	request := domain.AgentRequest{
		RequestID:  fmt.Sprintf("qq-%s-%d", state.messageID, eventTime(state.event)),
		SessionID:  state.sessionID,
		Text:       state.promptText,
		MessageID:  state.messageID,
		UserID:     state.userID,
		Platform:   state.platform,
		ChatType:   state.chatType,
		ChatID:     state.chatID,
		GroupID:    state.groupID,
		SelfID:     state.selfID,
		SenderName: state.event.SenderName,
		SenderRole: state.event.SenderRole,
		History:    append([]domain.ChatMessage(nil), state.history...),
		Chain:      state.event.Chain.Clone(),
	}
	if contextText, ok := event.Values["agent.prompt_context"].(string); ok {
		request.PromptContext = strings.TrimSpace(contextText)
	}
	if s.personas != nil {
		var (
			profile persona.Profile
			ok      bool
		)
		if state.personaName != "" {
			profile, ok = s.personas.Get(state.personaName)
		} else {
			profile, ok = s.personas.Default()
		}
		if ok {
			request.SystemPrompt = profile.SystemPrompt
			request.History = prependBeginDialogs(profile.BeginDialogs, request.History)
			if profile.CustomErrorMessage != "" {
				state.errorReply = profile.CustomErrorMessage
			}
		}
	}
	if appendPrompt, ok := event.Values["agent.system_prompt_append"].(string); ok {
		appendPrompt = strings.TrimSpace(appendPrompt)
		if appendPrompt != "" {
			if request.SystemPrompt != "" {
				request.SystemPrompt += "\n\n"
			}
			request.SystemPrompt += appendPrompt
		}
	}
	var (
		response domain.AgentResponse
		err      error
	)
	if s.providers != nil {
		if state.providerName != "" {
			response, err = s.providers.ReplyWith(ctx, state.providerName, request)
		} else {
			response, err = s.providers.Reply(ctx, request)
		}
	} else if s.agent != nil {
		response, err = s.agent.Reply(ctx, request)
	} else {
		err = fmt.Errorf("no agent provider is configured")
	}
	if err != nil {
		state.agentErr = err
		state.reply = state.errorReply
		s.stats.agentErrors.Add(1)
		s.logger.Error(
			"agent request failed",
			"chat_type", state.chatType,
			"chat_id", state.chatID,
			"user_id", state.userID,
			"message_id", state.messageID,
			"error", err,
		)
		return nil
	}
	state.response = response
	s.stats.processed.Add(1)
	return nil
}

func (s *Service) stageDecorate(_ context.Context, event *pipeline.Context) error {
	state := stateFrom(event)
	if state.ignored || state.sessionID == "" {
		return nil
	}
	if state.agentErr != nil {
		return nil
	}

	if state.pluginHandled {
		state.reply = strings.TrimSpace(state.reply)
		if state.reply == "" {
			state.agentErr = fmt.Errorf("plugin returned an empty reply")
			state.reply = state.errorReply
			s.stats.agentErrors.Add(1)
		}
		state.outbound = message.Chain{message.Text(state.reply)}
		return nil
	}

	state.reply = strings.TrimSpace(state.response.Reply)
	state.outbound = state.response.Chain.Clone()
	if state.reply == "" && !state.outbound.Empty() {
		state.reply, _ = state.outbound.PlainText("")
		state.reply = strings.TrimSpace(state.reply)
	}
	if state.response.Handoff {
		s.sessions.SetHandoff(state.sessionID, true)
		s.stats.handoffCount.Add(1)
		if state.reply == "" {
			state.reply = s.cfg.HandoffReply
			state.outbound = message.Chain{message.Text(state.reply)}
		}
	}
	if state.reply == "" {
		state.agentErr = fmt.Errorf("agent returned an empty reply")
		state.reply = state.errorReply
		s.stats.agentErrors.Add(1)
		s.logger.Error(
			"agent returned empty reply",
			"chat_type", state.chatType,
			"chat_id", state.chatID,
			"user_id", state.userID,
			"message_id", state.messageID,
		)
		return nil
	}
	if state.outbound.Empty() {
		state.outbound = message.Chain{message.Text(state.reply)}
	}
	return nil
}

func (s *Service) stagePluginBefore(ctx context.Context, event *pipeline.Context) error {
	state := stateFrom(event)
	if state.ignored || state.sessionID == "" || s.plugins == nil {
		return nil
	}
	pluginContext := &plugin.MessageContext{
		Event:     state.event,
		SessionID: state.sessionID,
		Text:      state.text,
		Values:    event.Values,
	}
	decision, err := s.plugins.Before(ctx, pluginContext)
	if err != nil {
		return err
	}
	if decision.Ignore {
		state.ignored = true
		event.Stop("plugin_ignored")
		return nil
	}
	if decision.Reply != "" || decision.Handled || decision.Handoff {
		state.pluginHandled = true
		state.reply = strings.TrimSpace(decision.Reply)
		if decision.Handoff {
			s.sessions.SetHandoff(state.sessionID, true)
			s.stats.handoffCount.Add(1)
			if state.reply == "" {
				state.reply = s.cfg.HandoffReply
			}
		}
		s.stats.processed.Add(1)
	}
	return nil
}

func (s *Service) stagePluginAfter(ctx context.Context, event *pipeline.Context) error {
	state := stateFrom(event)
	if state.ignored || state.reply == "" || s.plugins == nil {
		return nil
	}
	pluginContext := &plugin.MessageContext{
		Event:     state.event,
		SessionID: state.sessionID,
		Text:      state.text,
		Reply:     state.reply,
		Values:    event.Values,
	}
	originalReply := state.reply
	if err := s.plugins.After(ctx, pluginContext); err != nil {
		return err
	}
	state.reply = strings.TrimSpace(pluginContext.Reply)
	if state.reply == "" {
		state.agentErr = fmt.Errorf("plugin removed the reply")
		state.reply = state.errorReply
		state.outbound = message.Chain{message.Text(state.reply)}
		s.stats.agentErrors.Add(1)
	} else if state.reply != originalReply {
		state.outbound = replaceChainText(state.outbound, state.reply)
	}
	return nil
}

func (s *Service) stageRespond(ctx context.Context, event *pipeline.Context) error {
	state := stateFrom(event)
	if state.ignored || state.reply == "" {
		return nil
	}
	if state.agentErr == nil {
		s.sessions.AddExchange(state.sessionID, state.promptText, state.reply)
	}
	chain := state.outbound
	if chain.Empty() {
		chain = message.Chain{message.Text(state.reply)}
	}
	s.sendChain(
		ctx,
		state.selfID,
		state.chatType,
		state.chatID,
		state.userID,
		state.event.SenderName,
		state.messageID,
		chain,
	)
	return nil
}

func replaceChainText(chain message.Chain, text string) message.Chain {
	if chain.Empty() {
		return message.Chain{message.Text(text)}
	}
	result := make(message.Chain, 0, len(chain)+1)
	replaced := false
	for _, component := range chain {
		if component.Type == message.TypeText {
			if !replaced {
				result = append(result, message.Text(text))
				replaced = true
			}
			continue
		}
		result = append(result, component)
	}
	if !replaced {
		result = append(message.Chain{message.Text(text)}, result...)
	}
	return result
}

func groupPromptText(senderName, userID, text string) string {
	senderName = strings.TrimSpace(senderName)
	userID = strings.TrimSpace(userID)
	if senderName == "" {
		senderName = "QQ user"
	}
	return fmt.Sprintf("[%s (%s)]: %s", senderName, userID, strings.TrimSpace(text))
}

func prependBeginDialogs(dialogs []persona.Dialogue, history []domain.ChatMessage) []domain.ChatMessage {
	if len(dialogs) == 0 {
		return history
	}
	result := make([]domain.ChatMessage, 0, len(dialogs)*2+len(history))
	for _, dialogue := range dialogs {
		result = append(result,
			domain.ChatMessage{Role: "user", Content: dialogue.User},
			domain.ChatMessage{Role: "assistant", Content: dialogue.Assistant},
		)
	}
	return append(result, history...)
}

func eventTime(event platform.Event) int64 {
	if value, ok := event.Metadata["time"]; ok {
		switch typed := value.(type) {
		case int64:
			return typed
		case int:
			return int64(typed)
		case float64:
			return int64(typed)
		case json.Number:
			value, _ := typed.Int64()
			return value
		}
	}
	return 0
}
