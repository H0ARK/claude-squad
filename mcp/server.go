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
	ID                  string    `json:"id"`
	SessionID           string    `json:"session_id"`
	Task                string    `json:"task,omitempty"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	LastActive          time.Time `json:"last_active"`
	AutoRespond         bool      `json:"auto_respond"`
	OrchestratorSession string    `json:"orchestrator_session"`
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
		mcp.WithDescription("Get the last agent message or recent output from an agent's tmux session"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("ID of the agent to get output from"),
		),
		mcp.WithString("mode",
			mcp.Description("Output mode: 'last_message' (default), 'last_x_messages', or 'raw_lines'"),
		),
		mcp.WithNumber("count",
			mcp.Description("Number of messages/lines to retrieve (default: 1 for messages, 50 for raw lines)"),
		),
	)

	s.AddTool(getOutputTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agentID, err := request.RequireString("agent_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		mode := request.GetString("mode", "last_message")
		count := int(request.GetFloat("count", 1))

		// Set default count based on mode
		if mode == "raw_lines" && count == 1 {
			count = 50
		}

		result, err := agentManager.getAgentOutput(agentID, mode, count)
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

	// Add auto_respond tool for automatic responses to orchestrator
	autoRespondTool := mcp.NewTool("auto_respond",
		mcp.WithDescription("Enable/disable automatic response to orchestrator when task is completed"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("ID of the agent to configure auto-response for"),
		),
		mcp.WithBoolean("enabled",
			mcp.Description("Whether to enable auto-response (default: true)"),
		),
		mcp.WithString("orchestrator_session",
			mcp.Description("Target session for responses (default: 'claudesquad_orc')"),
		),
	)

	s.AddTool(autoRespondTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agentID, err := request.RequireString("agent_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		enabled := request.GetBool("enabled", true)
		orchestratorSession := request.GetString("orchestrator_session", "claudesquad_orc")

		result, err := agentManager.setAutoResponse(agentID, enabled, orchestratorSession)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
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
		ID:                  agentID,
		SessionID:           title, // Use title as session identifier
		Task:                task,
		Status:              "active",
		CreatedAt:           time.Now(),
		LastActive:          time.Now(),
		AutoRespond:         true,                // Enable auto-respond by default
		OrchestratorSession: "claudesquad_orc",  // Default orchestrator session
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

	// Trigger auto-response check if enabled
	if agent.AutoRespond {
		am.checkForTaskCompletion(agentID)
	}

	return fmt.Sprintf("Message sent to agent %s: %s", agentID, message), nil
}

func (am *AgentManager) getAgentOutput(agentID string, mode string, count int) (string, error) {
	am.mu.RLock()
	agent, exists := am.agents[agentID]
	am.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	// Use the formatted session name (same as sendMessage)
	sessionName := formatSessionName(agent.SessionID)

	switch mode {
	case "last_message":
		return am.getLastAgentMessage(sessionName, agentID)
	case "last_x_messages":
		return am.getLastXAgentMessages(sessionName, agentID, count)
	case "raw_lines":
		fallthrough
	default:
		// Capture tmux pane output
		cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", fmt.Sprintf("-%d", count))
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to capture output from agent %s: %w", agentID, err)
		}
		return fmt.Sprintf("Output from agent %s (last %d lines):\n%s", agentID, count, string(output)), nil
	}
}

// getLastAgentMessage extracts the last complete message from the agent
func (am *AgentManager) getLastAgentMessage(sessionName, agentID string) (string, error) {
	// Get more lines to ensure we capture complete messages
	cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", "-200")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture output from agent %s: %w", agentID, err)
	}

	lines := strings.Split(string(output), "\n")
	
	// Find the last substantive agent message by looking for patterns
	var messages []string
	var currentMessage strings.Builder
	inMessage := false
	
	for _, line := range lines {
		line = strings.TrimSpace(line)
		
		// Skip empty lines
		if line == "" {
			continue
		}
		
		// Skip prompts and UI elements
		if strings.HasPrefix(line, ">") || 
		   strings.HasPrefix(line, "?") ||
		   strings.Contains(line, "for shortcuts") ||
		   strings.Contains(line, "╭") ||
		   strings.Contains(line, "╰") ||
		   strings.Contains(line, "│") {
			// If we were building a message, save it
			if inMessage && currentMessage.Len() > 0 {
				messages = append(messages, strings.TrimSpace(currentMessage.String()))
				currentMessage.Reset()
				inMessage = false
			}
			continue
		}
		
		// This looks like actual content, start/continue building message
		if !inMessage {
			inMessage = true
		}
		
		if currentMessage.Len() > 0 {
			currentMessage.WriteString("\n")
		}
		currentMessage.WriteString(line)
	}
	
	// Add final message if we have one
	if inMessage && currentMessage.Len() > 0 {
		messages = append(messages, strings.TrimSpace(currentMessage.String()))
	}
	
	// Return the last meaningful message
	if len(messages) == 0 {
		return fmt.Sprintf("No recent substantive message found from agent %s", agentID), nil
	}
	
	lastMsg := messages[len(messages)-1]
	
	// Filter out very short or non-substantive messages
	if len(lastMsg) < 10 || 
	   strings.Contains(strings.ToLower(lastMsg), "trust") ||
	   strings.Contains(strings.ToLower(lastMsg), "file") && strings.Contains(strings.ToLower(lastMsg), "folder") {
		// Look for a better message
		for i := len(messages) - 2; i >= 0; i-- {
			candidate := messages[i]
			if len(candidate) >= 10 && 
			   !strings.Contains(strings.ToLower(candidate), "trust") &&
			   !(strings.Contains(strings.ToLower(candidate), "file") && strings.Contains(strings.ToLower(candidate), "folder")) {
				lastMsg = candidate
				break
			}
		}
	}
	
	return fmt.Sprintf("Last message from agent %s:\n%s", agentID, lastMsg), nil
}

// getLastXAgentMessages extracts the last X complete messages from the agent
func (am *AgentManager) getLastXAgentMessages(sessionName, agentID string, count int) (string, error) {
	// Get more lines to ensure we capture complete messages
	cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", "-300")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture output from agent %s: %w", agentID, err)
	}

	lines := strings.Split(string(output), "\n")
	var messages []string
	var currentMessage strings.Builder
	inMessage := false
	
	for _, line := range lines {
		line = strings.TrimSpace(line)
		
		// Skip empty lines
		if line == "" {
			continue
		}
		
		// Skip prompts and UI elements - same logic as getLastAgentMessage
		if strings.HasPrefix(line, ">") || 
		   strings.HasPrefix(line, "?") ||
		   strings.Contains(line, "for shortcuts") ||
		   strings.Contains(line, "╭") ||
		   strings.Contains(line, "╰") ||
		   strings.Contains(line, "│") {
			// If we were building a message, save it
			if inMessage && currentMessage.Len() > 0 {
				msg := strings.TrimSpace(currentMessage.String())
				// Only save substantial messages
				if len(msg) >= 10 && 
				   !strings.Contains(strings.ToLower(msg), "trust") &&
				   !(strings.Contains(strings.ToLower(msg), "file") && strings.Contains(strings.ToLower(msg), "folder")) {
					messages = append(messages, msg)
				}
				currentMessage.Reset()
				inMessage = false
			}
			continue
		}
		
		// This looks like actual content, start/continue building message
		if !inMessage {
			inMessage = true
		}
		
		if currentMessage.Len() > 0 {
			currentMessage.WriteString("\n")
		}
		currentMessage.WriteString(line)
	}
	
	// Add final message if we have one
	if inMessage && currentMessage.Len() > 0 {
		msg := strings.TrimSpace(currentMessage.String())
		if len(msg) >= 10 && 
		   !strings.Contains(strings.ToLower(msg), "trust") &&
		   !(strings.Contains(strings.ToLower(msg), "file") && strings.Contains(strings.ToLower(msg), "folder")) {
			messages = append(messages, msg)
		}
	}
	
	// Get the last 'count' messages
	start := len(messages) - count
	if start < 0 {
		start = 0
	}
	
	if len(messages) == 0 {
		return fmt.Sprintf("No recent substantive messages found from agent %s", agentID), nil
	}
	
	selectedMessages := messages[start:]
	result := strings.Join(selectedMessages, "\n\n--- Next Message ---\n\n")
	
	return fmt.Sprintf("Last %d message(s) from agent %s:\n%s", len(selectedMessages), agentID, result), nil
}

// Send agent output back to main Claude session
func (am *AgentManager) sendOutputToMain(agentID string) error {
	// Get agent output using the new last_message mode
	output, err := am.getAgentOutput(agentID, "last_message", 1)
	if err != nil {
		return err
	}

	// Send to main session (claudesquad_orc)
	cmd1 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", fmt.Sprintf("Agent %s completed task. %s", agentID, output))
	if err := cmd1.Run(); err != nil {
		return fmt.Errorf("failed to send output to main: %w", err)
	}

	cmd2 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", "C-m")
	if err := cmd2.Run(); err != nil {
		return fmt.Errorf("failed to send enter to main: %w", err)
	}

	return nil
}

// setAutoResponse configures automatic response settings for an agent
func (am *AgentManager) setAutoResponse(agentID string, enabled bool, orchestratorSession string) (string, error) {
	am.mu.Lock()
	defer am.mu.Unlock()

	agent, exists := am.agents[agentID]
	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	agent.AutoRespond = enabled
	agent.OrchestratorSession = orchestratorSession
	am.agents[agentID] = agent

	status := "disabled"
	if enabled {
		status = "enabled"
	}

	return fmt.Sprintf("Auto-response %s for agent %s (target: %s)", status, agentID, orchestratorSession), nil
}

// checkForTaskCompletion monitors an agent for task completion and sends auto-response
func (am *AgentManager) checkForTaskCompletion(agentID string) {
	go func() {
		// Wait a bit for the message to be processed
		time.Sleep(3 * time.Second)

		am.mu.RLock()
		agent, exists := am.agents[agentID]
		am.mu.RUnlock()

		if !exists || !agent.AutoRespond {
			return
		}

		// Get the latest output to see if task appears complete
		output, err := am.getAgentOutput(agentID, "last_message", 1)
		if err != nil {
			log.ErrorLog.Printf("Failed to check completion for agent %s: %v", agentID, err)
			return
		}

		// Simple heuristic: if the output doesn't contain "thinking" or "working" indicators,
		// and contains completion indicators, consider it done
		outputLower := strings.ToLower(output)
		completionIndicators := []string{"completed", "done", "finished", "ready", "result:", "output:", "here"}
		workingIndicators := []string{"thinking", "working", "processing", "analyzing", "let me", "i'll", "i will"}

		hasCompletion := false
		for _, indicator := range completionIndicators {
			if strings.Contains(outputLower, indicator) {
				hasCompletion = true
				break
			}
		}

		hasWorking := false
		for _, indicator := range workingIndicators {
			if strings.Contains(outputLower, indicator) {
				hasWorking = true
				break
			}
		}

		if hasCompletion && !hasWorking {
			// Send auto-response to orchestrator
			message := fmt.Sprintf("Agent %s has completed its task: %s", agentID, output)
			
			cmd1 := exec.Command("tmux", "send-keys", "-t", agent.OrchestratorSession, message)
			if err := cmd1.Run(); err != nil {
				log.ErrorLog.Printf("Failed to send auto-response to %s: %v", agent.OrchestratorSession, err)
				return
			}

			cmd2 := exec.Command("tmux", "send-keys", "-t", agent.OrchestratorSession, "C-m")
			if err := cmd2.Run(); err != nil {
				log.ErrorLog.Printf("Failed to send enter for auto-response: %v", err)
				return
			}

			log.InfoLog.Printf("Sent auto-response for agent %s to %s", agentID, agent.OrchestratorSession)
		}
	}()
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
		log.InfoLog.Printf("Adding MCP instance to storage: %s (started: %v)", instance.Title, instance.Started())
	}

	log.InfoLog.Printf("Saving total of %d instances to storage", len(allInstances))
	err = am.storage.SaveInstances(allInstances)
	if err != nil {
		log.ErrorLog.Printf("Failed to save instances to storage: %v", err)
		return err
	}
	log.InfoLog.Printf("Successfully saved all instances to storage")
	return nil
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
					ID:                  agentID,
					SessionID:           instance.Title,
					Task:                "", // Task info is lost on reload
					Status:              "active",
					CreatedAt:           instance.CreatedAt,
					LastActive:          instance.UpdatedAt,
					AutoRespond:         true,                // Default to enabled for loaded agents
					OrchestratorSession: "claudesquad_orc",  // Default orchestrator session
				}
			}
		}
	}

	return nil
}
