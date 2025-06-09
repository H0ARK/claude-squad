package mcp

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/session"
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
	agents   map[string]AgentInfo
	instances map[string]*session.Instance
	storage  *session.Storage
	mu      sync.RWMutex
}

func NewAgentManager() *AgentManager {
	// Initialize storage to share with main claude-squad UI
	appState := config.LoadState()
	storage, err := session.NewStorage(appState)
	if err != nil {
		log.ErrorLog.Printf("Failed to initialize MCP storage: %v", err)
		return nil
	}

	am := &AgentManager{
		agents:    make(map[string]AgentInfo),
		instances: make(map[string]*session.Instance),
		storage:   storage,
	}

	// Load existing instances from storage
	if err := am.loadInstancesFromStorage(); err != nil {
		log.ErrorLog.Printf("Failed to load existing instances: %v", err)
	}

	return am
}

// CreateMCPServer creates and configures the MCP server with agent management tools
func CreateMCPServer() *server.MCPServer {
	agentManager := NewAgentManager()
	if agentManager == nil {
		log.ErrorLog.Printf("Failed to create agent manager")
		return nil
	}

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
			mcp.Description("Program to run (default: 'claude -p')"),
		),
	)

	s.AddTool(launchAgentTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		task, err := request.RequireString("task")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		program := request.GetString("program", "claude")

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

	// Add send_to_main tool
	sendToMainTool := mcp.NewTool("send_to_main",
		mcp.WithDescription("Send agent output to main Claude session"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("ID of the agent to send output from"),
		),
	)

	s.AddTool(sendToMainTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agentID, err := request.RequireString("agent_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		err = agentManager.sendOutputToMain(agentID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Sent output from agent %s to main session", agentID)), nil
	})

	return s
}

// Agent management methods
func (am *AgentManager) launchAgent(task, program string) (string, error) {
	// Generate unique agent ID and title
	agentID := fmt.Sprintf("agent-%d", time.Now().Unix())
	title := fmt.Sprintf("Agent-%s", agentID)

	// Instead of creating a new instance, connect to existing claude-squad
	// by launching a new session in the current claude-squad instance
	actualProgram := "claude"
	if program != "" && program != "claude-squad" && program != "./claude-squad" {
		actualProgram = program
	}
	
	log.InfoLog.Printf("Creating agent %s as new session with program '%s' (requested: '%s')", agentID, actualProgram, program)
	
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   title,
		Path:    ".",
		Program: actualProgram,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create instance: %w", err)
	}

	// Start the instance (this creates the tmux session and shows in UI)
	if err := instance.Start(true); err != nil {
		return "", fmt.Errorf("failed to start instance: %w", err)
	}

	// Wait a moment for Claude Code to start up and show trust prompt
	time.Sleep(2 * time.Second)
	
	// Auto-accept trust prompt by sending "1"
	if err := instance.SendPrompt("1"); err != nil {
		log.ErrorLog.Printf("Failed to send trust response to agent %s: %v", agentID, err)
		// Don't kill on trust failure, continue
	} else {
		log.InfoLog.Printf("Sent trust response to agent %s", agentID)
	}
	
	// Wait for trust to be processed
	time.Sleep(1 * time.Second)
	
	// Send the initial task as a prompt to the agent
	if task != "" {
		if err := instance.SendPrompt(task); err != nil {
			log.ErrorLog.Printf("Failed to send initial prompt to agent %s: %v", agentID, err)
			instance.Kill()
			return "", fmt.Errorf("failed to send initial prompt to agent %s: %w", agentID, err)
		}
		log.InfoLog.Printf("Sent initial prompt to agent %s: %s", agentID, task)
	}

	// Store agent info
	am.mu.Lock()
	am.agents[agentID] = AgentInfo{
		ID:         agentID,
		SessionID:  title, // Use title as session identifier
		Task:       task,
		Status:     "active",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	am.instances[agentID] = instance
	am.mu.Unlock()

	// Save instance to shared storage so it appears in main UI
	if err := am.saveInstancesToStorage(); err != nil {
		log.ErrorLog.Printf("Failed to save instances to storage: %v", err)
	} else {
		log.InfoLog.Printf("Successfully saved agent %s to storage for UI sync", agentID)
	}

	return fmt.Sprintf("Agent %s created as new session '%s' with task: %s", agentID, title, task), nil
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
		// Check if tmux session still exists using formatted session name
		sessionName := formatSessionName(agent.SessionID)
		cmd := exec.Command("tmux", "has-session", "-t", sessionName)
		status := "active"
		if err := cmd.Run(); err != nil {
			status = "inactive"
		}

		result.WriteString(fmt.Sprintf("- %s (Session: %s, Status: %s, Task: %s, Created: %s)\n",
			agent.ID, sessionName, status, agent.Task, agent.CreatedAt.Format("15:04:05")))
	}

	return result.String()
}

// formatSessionName converts a title to the tmux session name used by claude-squad
func formatSessionName(title string) string {
	return fmt.Sprintf("claudesquad_%s", strings.ReplaceAll(title, " ", ""))
}

func (am *AgentManager) sendMessage(agentID, message string) (string, error) {
	am.mu.RLock()
	agent, exists := am.agents[agentID]
	am.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	instance, ok := am.instances[agentID]
	if !ok {
		return "", fmt.Errorf("agent %s has no instance", agentID)
	}

	// Send message using claude-squad instance tmux session (two-step: text first, then enter)
	// Access the tmux session directly from the instance
	if !instance.Started() {
		return "", fmt.Errorf("agent %s instance not started", agentID)
	}

	// Use the formatted session name
	sessionName := formatSessionName(instance.Title)

	cmd1 := exec.Command("tmux", "send-keys", "-t", sessionName, message)
	if err := cmd1.Run(); err != nil {
		return "", fmt.Errorf("failed to send message to agent %s: %w", agentID, err)
	}

	cmd2 := exec.Command("tmux", "send-keys", "-t", sessionName, "C-m")
	if err := cmd2.Run(); err != nil {
		return "", fmt.Errorf("failed to send enter to agent %s: %w", agentID, err)
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

	// Use the formatted session name (same as sendMessage)
	sessionName := formatSessionName(agent.SessionID)

	// Capture tmux pane output
	cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", fmt.Sprintf("-%d", lines))
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture output from agent %s: %w", agentID, err)
	}

	return fmt.Sprintf("Output from agent %s (last %d lines):\n%s", agentID, lines, string(output)), nil
}

// Send agent output back to main Claude session
func (am *AgentManager) sendOutputToMain(agentID string) error {
	// Get agent output
	output, err := am.getAgentOutput(agentID, 20)
	if err != nil {
		return err
	}

	// Send to main session (claudesquad_orc)
	cmd1 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", fmt.Sprintf("Agent %s output: %s", agentID, output))
	if err := cmd1.Run(); err != nil {
		return fmt.Errorf("failed to send output to main: %w", err)
	}

	cmd2 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", "C-m")
	if err := cmd2.Run(); err != nil {
		return fmt.Errorf("failed to send enter to main: %w", err)
	}

	return nil
}

// saveInstancesToStorage saves all current instances to the shared storage
func (am *AgentManager) saveInstancesToStorage() error {
	am.mu.RLock()
	defer am.mu.RUnlock()

	log.InfoLog.Printf("Saving %d MCP instances to storage", len(am.instances))

	// Load existing instances to merge with MCP instances
	existingInstances, err := am.storage.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load existing instances: %w", err)
	}
	log.InfoLog.Printf("Loaded %d existing instances from storage", len(existingInstances))

	// Create a map of existing non-MCP instances
	nonMCPInstances := make(map[string]*session.Instance)
	for _, instance := range existingInstances {
		if !strings.HasPrefix(instance.Title, "Agent-agent-") {
			nonMCPInstances[instance.Title] = instance
		}
	}
	log.InfoLog.Printf("Found %d non-MCP instances to preserve", len(nonMCPInstances))

	// Combine non-MCP instances with current MCP instances
	var allInstances []*session.Instance
	
	// Add non-MCP instances
	for _, instance := range nonMCPInstances {
		allInstances = append(allInstances, instance)
	}
	
	// Add current MCP instances
	for _, instance := range am.instances {
		allInstances = append(allInstances, instance)
		log.InfoLog.Printf("Adding MCP instance to storage: %s", instance.Title)
	}

	log.InfoLog.Printf("Saving total of %d instances to storage", len(allInstances))
	return am.storage.SaveInstances(allInstances)
}

// loadInstancesFromStorage loads existing instances from shared storage
func (am *AgentManager) loadInstancesFromStorage() error {
	instances, err := am.storage.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	am.mu.Lock()
	defer am.mu.Unlock()

	// Only load instances that look like they were created by MCP (have Agent- prefix)
	for _, instance := range instances {
		if strings.HasPrefix(instance.Title, "Agent-agent-") {
			// Extract agent ID from title (format: Agent-agent-1234567890)
			parts := strings.Split(instance.Title, "-")
			if len(parts) >= 3 {
				agentID := strings.Join(parts[1:], "-") // agent-1234567890
				am.instances[agentID] = instance
				am.agents[agentID] = AgentInfo{
					ID:         agentID,
					SessionID:  instance.Title,
					Task:       "", // Task info is lost on reload
					Status:     "active",
					CreatedAt:  instance.CreatedAt,
					LastActive: instance.UpdatedAt,
				}
			}
		}
	}

	return nil
}
