package mcp

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Agent management types
type AgentInfo struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Task       string    `json:"task,omitempty"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

type AgentManager struct {
	agents map[string]AgentInfo
	mu     sync.RWMutex
}

func NewAgentManager() *AgentManager {
	return &AgentManager{
		agents: make(map[string]AgentInfo),
	}
}

// CreateMCPServer creates and configures the MCP server with agent management tools
func CreateMCPServer() *server.MCPServer {
	agentManager := NewAgentManager()

	// Create the MCP server
	s := server.NewMCPServer(
		"claude-squad",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	// Add launch_agent tool
	launchAgentTool := mcp.NewTool("launch_agent",
		mcp.WithDescription("Launch a new agent in a tmux session"),
		mcp.WithString("task",
			mcp.Required(),
			mcp.Description("Description of the task for the new agent"),
		),
		mcp.WithString("program",
			mcp.Description("Program to run (e.g., 'aider', 'cursor')"),
		),
	)

	s.AddTool(launchAgentTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		task, err := request.RequireString("task")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		program := request.GetString("program", "aider")

		result, err := agentManager.launchAgent(task, program)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	})

	// Add list_agents tool
	listAgentsTool := mcp.NewTool("list_agents",
		mcp.WithDescription("List all active agents and their status"),
	)

	s.AddTool(listAgentsTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result := agentManager.listAgents()
		return mcp.NewToolResultText(result), nil
	})

	// Add send_message tool
	sendMessageTool := mcp.NewTool("send_message",
		mcp.WithDescription("Send a message to a specific agent via tmux"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("ID of the target agent"),
		),
		mcp.WithString("message",
			mcp.Required(),
			mcp.Description("Message to send to the agent"),
		),
	)

	s.AddTool(sendMessageTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agentID, err := request.RequireString("agent_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		message, err := request.RequireString("message")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := agentManager.sendMessage(agentID, message)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	})

	// Add get_agent_output tool
	getOutputTool := mcp.NewTool("get_agent_output",
		mcp.WithDescription("Get recent output from an agent's tmux session"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("ID of the agent to get output from"),
		),
		mcp.WithNumber("lines",
			mcp.Description("Number of lines to retrieve (default: 50)"),
		),
	)

	s.AddTool(getOutputTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agentID, err := request.RequireString("agent_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		lines := int(request.GetFloat("lines", 50))

		result, err := agentManager.getAgentOutput(agentID, lines)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	})

	return s
}

// Agent management methods
func (am *AgentManager) launchAgent(task, program string) (string, error) {
	// Generate unique agent ID
	agentID := fmt.Sprintf("agent-%d", time.Now().Unix())
	sessionID := fmt.Sprintf("claude-squad-%s", agentID)

	// Create tmux session
	cmd := exec.Command("tmux", "new-session", "-d", "-s", sessionID, program)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Store agent info
	am.mu.Lock()
	am.agents[agentID] = AgentInfo{
		ID:         agentID,
		SessionID:  sessionID,
		Task:       task,
		Status:     "active",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	am.mu.Unlock()

	return fmt.Sprintf("Agent %s launched successfully in tmux session %s with task: %s", agentID, sessionID, task), nil
}

func (am *AgentManager) listAgents() string {
	am.mu.RLock()
	defer am.mu.RUnlock()

	if len(am.agents) == 0 {
		return "No active agents"
	}

	var result strings.Builder
	result.WriteString("Active agents:\n")

	for _, agent := range am.agents {
		// Check if tmux session still exists
		cmd := exec.Command("tmux", "has-session", "-t", agent.SessionID)
		status := "active"
		if err := cmd.Run(); err != nil {
			status = "inactive"
		}

		result.WriteString(fmt.Sprintf("- %s (Session: %s, Status: %s, Task: %s, Created: %s)\n",
			agent.ID, agent.SessionID, status, agent.Task, agent.CreatedAt.Format("15:04:05")))
	}

	return result.String()
}

func (am *AgentManager) sendMessage(agentID, message string) (string, error) {
	am.mu.RLock()
	agent, exists := am.agents[agentID]
	am.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	// Send message to tmux session
	cmd := exec.Command("tmux", "send-keys", "-t", agent.SessionID, message, "Enter")
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to send message to agent %s: %w", agentID, err)
	}

	// Update last active time
	am.mu.Lock()
	agent.LastActive = time.Now()
	am.agents[agentID] = agent
	am.mu.Unlock()

	return fmt.Sprintf("Message sent to agent %s: %s", agentID, message), nil
}

func (am *AgentManager) getAgentOutput(agentID string, lines int) (string, error) {
	am.mu.RLock()
	agent, exists := am.agents[agentID]
	am.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	// Capture tmux pane output
	cmd := exec.Command("tmux", "capture-pane", "-t", agent.SessionID, "-p", "-S", fmt.Sprintf("-%d", lines))
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture output from agent %s: %w", agentID, err)
	}

	return fmt.Sprintf("Output from agent %s (last %d lines):\n%s", agentID, lines, string(output)), nil
}
