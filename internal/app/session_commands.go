package app

import (
	"context"
	"strings"

	"actrail/internal/adapters/piagentgrpc"
	"actrail/internal/domain/session"
)

type SessionCommand struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Source      string         `json:"source"`
	SourceInfo  map[string]any `json:"source_info,omitempty"`
}

const sessionCommandSourceActRail = "actrail"

type SessionCommandsRequest struct {
	SessionID session.SessionID
}

type SessionCommandsResponse struct {
	Commands []SessionCommand `json:"commands"`
}

type ExecuteSessionCommandRequest struct {
	SessionID session.SessionID
	Name      string
	Args      string
}

type ExecuteSessionCommandResponse struct {
	OK        bool   `json:"ok"`
	Command   string `json:"command"`
	Message   string `json:"message,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

func actrailSlashCommands() []SessionCommand {
	return []SessionCommand{
		{Name: "rename", Description: "Rename current session", Source: sessionCommandSourceActRail},
		{Name: "focus", Description: "Mark current session focused or unfocused", Source: sessionCommandSourceActRail},
		{Name: "restart", Description: "Restart current runtime", Source: sessionCommandSourceActRail},
		{Name: "handoff", Description: "Start a fresh Pi runtime for this session", Source: sessionCommandSourceActRail},
		{Name: "model", Description: "Switch session model", Source: sessionCommandSourceActRail},
	}
}

func (s *Stub) SessionCommands(ctx context.Context, req SessionCommandsRequest) (SessionCommandsResponse, error) {
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return SessionCommandsResponse{}, err
	}
	commands := actrailSlashCommands()
	if record.identity.Backend() == session.BackendPI {
		if record.runtime.piAgentGRPC != nil {
			piCommands, err := record.runtime.piAgentGRPC.ListCommands(ctx)
			if err != nil {
				return SessionCommandsResponse{}, err
			}
			commands = append(commands, sessionCommandsFromPIAgentGRPC(piCommands)...)
		} else {
			commands = append(commands,
				SessionCommand{Name: "name", Description: "Set Pi session display name when supported by runtime", Source: "builtin"},
				SessionCommand{Name: "help", Description: "Show runtime command help when supported by runtime", Source: "builtin"},
			)
		}
	}
	return SessionCommandsResponse{Commands: commands}, nil
}

func sessionCommandsFromPIAgentGRPC(commands []piagentgrpc.Command) []SessionCommand {
	out := make([]SessionCommand, 0, len(commands))
	for _, command := range commands {
		out = append(out, SessionCommand{
			Name:        command.Name,
			Description: command.Description,
			Source:      command.Source,
			SourceInfo:  sourceInfoMapFromPIAgentGRPC(command.SourceInfo),
		})
	}
	return out
}

func sourceInfoMapFromPIAgentGRPC(sourceInfo piagentgrpc.SourceInfo) map[string]any {
	out := map[string]any{}
	if sourceInfo.Path != "" {
		out["path"] = sourceInfo.Path
	}
	if sourceInfo.Source != "" {
		out["source"] = sourceInfo.Source
	}
	if sourceInfo.Scope != "" {
		out["scope"] = sourceInfo.Scope
	}
	if sourceInfo.Origin != "" {
		out["origin"] = sourceInfo.Origin
	}
	if sourceInfo.BaseDir != "" {
		out["base_dir"] = sourceInfo.BaseDir
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s *Stub) ExecuteSessionCommand(ctx context.Context, req ExecuteSessionCommandRequest) (ExecuteSessionCommandResponse, error) {
	name := strings.TrimLeft(strings.TrimSpace(req.Name), "/")
	actrailName := strings.ToLower(name)
	args := strings.TrimSpace(req.Args)
	if name == "" {
		return ExecuteSessionCommandResponse{}, Invalid("command", "command is required")
	}
	record, err := s.lookupSession(req.SessionID)
	if err != nil {
		return ExecuteSessionCommandResponse{}, err
	}
	switch actrailName {
	case "rename":
		if args == "" {
			return ExecuteSessionCommandResponse{}, Invalid("args", "rename requires a name")
		}
		payload, err := s.RenameSession(ctx, RenameSessionRequest{SessionID: req.SessionID, Name: args})
		if err != nil {
			return ExecuteSessionCommandResponse{}, err
		}
		return ExecuteSessionCommandResponse{OK: payload.OK, Command: name, Message: "renamed", SessionID: req.SessionID.String()}, nil
	case "focus":
		focused := true
		if args == "0" || strings.EqualFold(args, "false") || strings.EqualFold(args, "off") || strings.EqualFold(args, "unfocus") {
			focused = false
		}
		payload, err := s.FocusSession(ctx, FocusSessionRequest{SessionID: req.SessionID, Focused: focused})
		if err != nil {
			return ExecuteSessionCommandResponse{}, err
		}
		return ExecuteSessionCommandResponse{OK: payload.OK, Command: name, Message: "focus updated", SessionID: req.SessionID.String()}, nil
	case "restart":
		payload, err := s.RestartSession(ctx, RestartSessionRequest{SessionID: req.SessionID})
		if err != nil {
			return ExecuteSessionCommandResponse{}, err
		}
		return ExecuteSessionCommandResponse{OK: payload.OK, Command: name, Message: "restart requested", SessionID: payload.SessionID}, nil
	case "handoff":
		payload, err := s.HandoffSession(ctx, HandoffSessionRequest{SessionID: req.SessionID})
		if err != nil {
			return ExecuteSessionCommandResponse{}, err
		}
		return ExecuteSessionCommandResponse{OK: payload.OK, Command: name, Message: "handoff requested", SessionID: payload.SessionID}, nil
	case "model":
		if args == "" {
			return ExecuteSessionCommandResponse{}, Invalid("args", "model requires a model name")
		}
		payload, err := s.SwitchSessionModel(ctx, SwitchSessionModelRequest{SessionID: req.SessionID, Model: StringPatch{Present: true, Value: &args}})
		if err != nil {
			return ExecuteSessionCommandResponse{}, err
		}
		message := "model updated"
		if payload.RestartRequired {
			message = "model saved; restart required"
		}
		return ExecuteSessionCommandResponse{OK: payload.OK, Command: name, Message: message, SessionID: req.SessionID.String()}, nil
	default:
		if record.identity.Backend() == session.BackendPI {
			if record.runtime.piAgentGRPC != nil {
				if err := record.runtime.piAgentGRPC.ExecuteCommand(ctx, name, args); err != nil {
					return ExecuteSessionCommandResponse{}, err
				}
				return ExecuteSessionCommandResponse{OK: true, Command: name, Message: "executed by runtime", SessionID: req.SessionID.String()}, nil
			}
			text := "/" + name
			if args != "" {
				text += " " + args
			}
			if _, err := s.Send(ctx, SendRequest{SessionID: req.SessionID, Text: text}); err != nil {
				return ExecuteSessionCommandResponse{}, err
			}
			return ExecuteSessionCommandResponse{OK: true, Command: name, Message: "sent to runtime", SessionID: req.SessionID.String()}, nil
		}
		return ExecuteSessionCommandResponse{}, NotFound("unknown command")
	}
}
